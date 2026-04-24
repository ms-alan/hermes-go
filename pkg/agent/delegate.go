package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nousresearch/hermes-go/pkg/model"
)

// DelegateRequest describes a single sub-agent task.
type DelegateRequest struct {
	// Goal is the task description for the sub-agent.
	Goal string
	// Context is optional background information.
	Context string
	// Toolsets restricts which tool categories are available to the sub-agent.
	// Comma-separated list: "terminal,file,web" etc. Empty = all available.
	Toolsets string
	// Role is "leaf" (cannot delegate further) or "orchestrator" (can delegate).
	Role string
	// Model overrides the model for this sub-agent (e.g. "gpt-4o").
	// Empty = use default model from environment.
	Model string
	// MaxIterations limits tool-call loops (default 50).
	MaxIterations int
	// TimeoutSecs kills the sub-agent after N seconds (default 300).
	TimeoutSecs int
}

// DelegateResult holds the outcome of a single delegated task.
type DelegateResult struct {
	Goal     string `json:"goal"`
	Status   string `json:"status"` // "success" | "error" | "timeout"
	Result   string `json:"result"`
	Error    string `json:"error,omitempty"`
	Duration int    `json:"duration_s"`
}

// Delegate runs one or more sub-agents concurrently and returns their results.
// It creates a fresh AIAgent per task with the specified configuration,
// runs each to completion (or timeout), and returns all results.
func Delegate(ctx context.Context, requests ...*DelegateRequest) ([]*DelegateResult, error) {
	if len(requests) == 0 {
		return nil, nil
	}

	type pending struct {
		req    *DelegateRequest
		result chan *DelegateResult
	}

	var pendingTasks []pending
	for _, req := range requests {
		if req == nil {
			continue
		}
		pendingTasks = append(pendingTasks, pending{req: req, result: make(chan *DelegateResult, 1)})
	}

	results := make([]*DelegateResult, 0, len(pendingTasks))

	// Run each task in its own goroutine.
	for _, p := range pendingTasks {
		p := p // capture loop var
		go func() {
			r := runDelegateTask(p.req)
			p.result <- r
		}()
	}

	// Collect results in order.
	for _, p := range pendingTasks {
		select {
		case result := <-p.result:
			results = append(results, result)
		case <-ctx.Done():
			results = append(results, &DelegateResult{
				Goal:   p.req.Goal,
				Status: "error",
				Error:  "parent context cancelled",
			})
		}
	}

	return results, nil
}

// runDelegateTask creates a sub-agent and runs it to completion.
func runDelegateTask(req *DelegateRequest) *DelegateResult {
	start := time.Now()
	goal := req.Goal
	if req.Context != "" {
		goal = req.Context + "\n\nTask: " + goal
	}

	timeout := time.Duration(req.TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 300 * time.Second
	}
	if req.MaxIterations == 0 {
		req.MaxIterations = 50
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	modelName := req.Model
	if modelName == "" {
		modelName = "gpt-4o"
	}

	client, baseURL, apiKey := resolveModelClient(modelName)
	if client == nil {
		return &DelegateResult{
			Goal:   req.Goal,
			Status: "error",
			Error:  "failed to create LLM client: check API key env vars",
		}
	}

	cfg := Config{
		Model:          modelName,
		MaxIterations:  req.MaxIterations,
		BaseURL:        baseURL,
		APIKey:         apiKey,
		TimeoutSeconds: int(timeout.Seconds()),
		Logger:         &slogLogger{log: slog.Default()},
	}

	agent := NewAIAgent(client, cfg)

	role := req.Role
	if role == "" {
		role = "leaf"
	}
	systemPrompt := buildDelegateSystemPrompt(role, req.Toolsets)

	runResult := agent.RunWithMessages(ctx, []*model.Message{}, systemPrompt)

	duration := int(time.Since(start).Seconds())
	if runResult.Error != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return &DelegateResult{
				Goal:     req.Goal,
				Status:   "timeout",
				Duration: duration,
			}
		}
		return &DelegateResult{
			Goal:     req.Goal,
			Status:   "error",
			Error:    runResult.Error.Error(),
			Duration: duration,
		}
	}

	finalText := ""
	if len(runResult.Messages) > 0 {
		last := runResult.Messages[len(runResult.Messages)-1]
		if last.Role == "assistant" && len(last.Content) > 0 {
			finalText = last.Content
		}
	}

	return &DelegateResult{
		Goal:     req.Goal,
		Status:   "success",
		Result:   finalText,
		Duration: duration,
	}
}

// resolveModelClient returns an LLMClient, baseURL, and apiKey for the given model name.
func resolveModelClient(modelName string) (model.LLMClient, string, string) {
	modelLower := strings.ToLower(modelName)

	switch {
	case strings.HasPrefix(modelLower, "anthropic/") ||
		strings.HasPrefix(modelLower, "claude"):
		apiKey := getEnvOr("ANTHROPIC_API_KEY", "")
		client, _ := model.NewOpenAIClient(
			model.WithBaseURL("https://api.anthropic.com"),
			model.WithAPIKey(apiKey),
			model.WithModel(modelName),
		)
		return client, "https://api.anthropic.com", apiKey

	case strings.HasPrefix(modelLower, "groq/"):
		apiKey := getEnvOr("GROQ_API_KEY", "")
		client, _ := model.NewOpenAIClient(
			model.WithBaseURL("https://api.groq.com/openai/v1"),
			model.WithAPIKey(apiKey),
			model.WithModel(modelName),
		)
		return client, "https://api.groq.com/openai/v1", apiKey

	case strings.HasPrefix(modelLower, "ollama/"):
		client, _ := model.NewOpenAIClient(
			model.WithBaseURL("http://localhost:11434"),
			model.WithAPIKey("ollama"),
			model.WithModel(strings.TrimPrefix(modelName, "ollama/")),
		)
		return client, "http://localhost:11434", "ollama"

	case strings.HasPrefix(modelLower, "openrouter/"):
		apiKey := getEnvOr("OPENROUTER_API_KEY", "")
		client, _ := model.NewOpenAIClient(
			model.WithBaseURL("https://openrouter.ai/api/v1"),
			model.WithAPIKey(apiKey),
			model.WithModel(modelName),
		)
		return client, "https://openrouter.ai/api/v1", apiKey

	case strings.HasPrefix(modelLower, "gemini"):
		apiKey := getEnvOr("GEMINI_API_KEY", "")
		client, _ := model.NewOpenAIClient(
			model.WithBaseURL("https://generativelanguage.googleapis.com"),
			model.WithAPIKey(apiKey),
			model.WithModel(modelName),
		)
		return client, "https://generativelanguage.googleapis.com", apiKey

	default:
		apiKey := getEnvOr("OPENAI_API_KEY", "")
		baseURL := getEnvOr("OPENAI_BASE_URL", "https://api.openai.com/v1")
		client, _ := model.NewOpenAIClient(
			model.WithBaseURL(baseURL),
			model.WithAPIKey(apiKey),
			model.WithModel(modelName),
		)
		return client, baseURL, apiKey
	}
}

// buildDelegateSystemPrompt constructs the system prompt for a sub-agent.
func buildDelegateSystemPrompt(role, toolsets string) string {
	roleDesc := "a helpful AI assistant that can use tools to accomplish tasks."
	if role == "orchestrator" {
		roleDesc = "an orchestrator AI that can delegate subtasks to specialized workers using the delegate_task tool."
	}

	prompt := fmt.Sprintf("You are %s\n\n", roleDesc)

	if toolsets != "" {
		prompt += fmt.Sprintf("You have access to the following tool categories: %s.\n", toolsets)
		prompt += "Do not use tools outside these categories unless absolutely necessary.\n"
	} else {
		prompt += "You have access to all available tools.\n"
	}

	prompt += "\nWork autonomously to accomplish the task assigned to you. Provide a clear final answer."
	return prompt
}

func getEnvOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

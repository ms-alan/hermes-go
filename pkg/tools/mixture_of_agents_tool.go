package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/nousresearch/hermes-go/pkg/model"
)

var mixtureOfAgentsSchema = map[string]any{
	"name":        "mixture_of_agents",
	"description": "Solve complex tasks using multiple LLMs in a layered architecture: reference models generate diverse responses in parallel, then an aggregator synthesizes them into a high-quality answer. Best for difficult reasoning, coding, and analytical problems.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "The complex user query or task to solve",
			},
			"reference_models": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Model names for reference responses (default: openai/gpt-4o, anthropic/claude-sonnet-4, google/gemini-2.0-flash)",
			},
			"aggregator_model": map[string]any{
				"type":        "string",
				"description": "Model name for final synthesis (default: openai/gpt-4o)",
			},
		},
		"required": []any{"prompt"},
	},
}

// mixtureOfAgentsHandler handles MoA requests.
func mixtureOfAgentsHandler(args map[string]any) string {
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return toolError("prompt is required")
	}

	// Reference models (diverse frontier models)
	refModels := []string{
		"openai/gpt-4o",
		"anthropic/claude-sonnet-4",
		"google/gemini-2.0-flash",
	}
	if rm, ok := args["reference_models"].([]any); ok && len(rm) > 0 {
		refModels = nil
		for _, m := range rm {
			if s, ok := m.(string); ok {
				refModels = append(refModels, s)
			}
		}
		if len(refModels) == 0 {
			refModels = []string{"openai/gpt-4o", "anthropic/claude-sonnet-4", "google/gemini-2.0-flash"}
		}
	}

	aggModel := "openai/gpt-4o"
	if am, ok := args["aggregator_model"].(string); ok && am != "" {
		aggModel = am
	}

	// Build OpenAI-compatible client for OpenRouter
	apiKey := getEnvOr("OPENROUTER_API_KEY", getEnvOr("OPENAI_API_KEY", ""))
	baseURL := getEnvOr("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1")

	client, err := model.NewOpenAIClient(
		model.WithBaseURL(baseURL),
		model.WithAPIKey(apiKey),
	)
	if err != nil {
		return toolError(fmt.Sprintf("failed to create LLM client: %v", err))
	}
	defer client.Close()

	// Run reference models in parallel
	type refResult struct {
		model   string
		content string
		err     error
	}
	refChan := make(chan refResult, len(refModels))
	var wg sync.WaitGroup
	for _, modelName := range refModels {
		wg.Add(1)
		go func(m string) {
			defer wg.Done()
			ctx := context.Background()
			resp, err := client.Chat(ctx, &model.ChatRequest{
				Model: m,
				Messages: []*model.Message{
					{Role: "user", Content: prompt},
				},
				Temperature: 0.6,
				MaxTokens:   4096,
			})
			if err != nil {
				refChan <- refResult{model: m, err: err}
				return
			}
			content := ""
			if len(resp.Choices) > 0 && resp.Choices[0].Message != nil {
				content = resp.Choices[0].Message.Content
			}
			refChan <- refResult{model: m, content: content}
		}(modelName)
	}
	go func() {
		wg.Wait()
		close(refChan)
	}()

	var refResults []refResult
	for r := range refChan {
		refResults = append(refResults, r)
	}

	// Build responses text for aggregator
	var responseTexts []string
	for _, r := range refResults {
		if r.err != nil {
			responseTexts = append(responseTexts, fmt.Sprintf("[%s error: %v]", r.model, r.err))
		} else {
			responseTexts = append(responseTexts, r.content)
		}
	}

	aggregatorPrompt := fmt.Sprintf(`You have been provided with a set of responses from various open-source models to the latest user query. Your task is to synthesize these responses into a single, high-quality response. It is crucial to critically evaluate the information provided in these responses, recognizing that some of it may be biased or incorrect. Your response should not simply replicate the given answers but should offer a refined, accurate, and comprehensive reply to the instruction. Ensure your response is well-structured, coherent, and adheres to the highest standards of accuracy and reliability.

Responses from models:

%s`, joinResponses(responseTexts))

	// Run aggregator
	ctx := context.Background()
	aggResp, err := client.Chat(ctx, &model.ChatRequest{
		Model: aggModel,
		Messages: []*model.Message{
			{Role: "system", Content: aggregatorPrompt},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.4,
		MaxTokens:   8192,
	})
	if err != nil {
		return toolError(fmt.Sprintf("aggregator call failed: %v", err))
	}

	finalContent := ""
	if len(aggResp.Choices) > 0 && aggResp.Choices[0].Message != nil {
		finalContent = aggResp.Choices[0].Message.Content
	}

	// Build reference summary
	refSummary := make([]map[string]any, 0, len(refResults))
	for _, r := range refResults {
		status := "success"
		if r.err != nil {
			status = "error"
		}
		refSummary = append(refSummary, map[string]any{
			"model":  r.model,
			"status": status,
		})
	}

	return toolResultData(map[string]any{
		"final_response":   finalContent,
		"reference_results": refSummary,
		"aggregator_model":  aggModel,
		"num_references":    len(refResults),
	})
}

func joinResponses(responses []string) string {
	var b strings.Builder
	for i, r := range responses {
		fmt.Fprintf(&b, "%d. %s\n\n", i+1, r)
	}
	return b.String()
}



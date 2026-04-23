package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nousresearch/hermes-go/pkg/model"
)

// ToolDef describes a tool available to the agent (name, description, input schema).
type ToolDef struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// AIAgent is the core agent that runs the conversation loop with tool calling.
type AIAgent struct {
	client    model.LLMClient
	config    Config
	logger    Logger
	tools     map[string]ToolHandler
	toolDefs  map[string]*ToolDef // tool metadata for introspection
}

// ToolHandler is a function that handles a tool call and returns a result.
type ToolHandler func(ctx context.Context, req *model.ToolCallRequest) *model.ToolResult

// NewAIAgent creates a new AIAgent with the given LLM client and config.
func NewAIAgent(client model.LLMClient, cfg Config) *AIAgent {
	cfg.Defaults()
	return &AIAgent{
		client:   client,
		config:   cfg,
		logger:   cfg.Logger,
		tools:    make(map[string]ToolHandler),
		toolDefs: make(map[string]*ToolDef),
	}
}

// RegisterTool registers a tool handler and its metadata.
func (a *AIAgent) RegisterTool(name string, handler ToolHandler, def *ToolDef) {
	a.tools[name] = handler
	if def != nil {
		a.toolDefs[name] = def
	}
}

// GetToolDefs returns the tool metadata map for introspection.
func (a *AIAgent) GetToolDefs() map[string]*ToolDef {
	return a.toolDefs
}

// GetToolHandler returns the tool handler for a given name, or nil.
func (a *AIAgent) GetToolHandler(name string) ToolHandler {
	return a.tools[name]
}

// RunResult holds the result of a conversation run.
type RunResult struct {
	FinalResponse string
	Messages      []*model.Message
	Iterations    int
	Error         error
}

// RunWithMessages runs a conversation loop with the given messages and system prompt.
// The messages slice is used as the starting point; new messages are appended during the loop.
func (a *AIAgent) RunWithMessages(ctx context.Context, messages []*model.Message, systemPrompt string) *RunResult {
	// Prepend system prompt if provided
	if systemPrompt != "" {
		messages = append([]*model.Message{model.SystemMessage(systemPrompt)}, messages...)
	}

	iteration := 0
	result := &RunResult{Messages: messages}

	for iteration < a.config.MaxIterations {
		iteration++
		result.Iterations = iteration

		a.logger.Info("iteration start", "iteration", iteration)

		// Build request
		req := &model.ChatRequest{
			Model:    a.config.Model,
			Messages: messages,
			Tools:    a.config.Tools,
		}

		// Debug: print context before LLM call
		if debugJSON, err := json.MarshalIndent(req, "", "  "); err == nil {
			a.logger.Info(string(debugJSON))
		}

		// Call the LLM
		resp, err := a.client.Chat(ctx, req)
		if err != nil {
			result.Error = err
			a.logger.Error("chat error", "error", err)
			return result
		}

		if len(resp.Choices) == 0 {
			result.Error = fmt.Errorf("no choices returned")
			return result
		}

		choice := resp.Choices[0]
		assistantMsg := choice.Message
		messages = append(messages, assistantMsg)

		// Check for tool calls
		if len(assistantMsg.ToolCalls) > 0 {
			a.logger.Info("tool calls detected", "count", len(assistantMsg.ToolCalls))

			for _, tc := range assistantMsg.ToolCalls {
				toolName := tc.Function.Name
				args := string(tc.Function.Arguments)

				// Parse arguments into a map
				var argsMap map[string]any
				if err := json.Unmarshal([]byte(args), &argsMap); err != nil {
					a.logger.Warn("failed to parse tool arguments", "tool", toolName, "error", err)
					argsMap = nil
				}

				toolReq := &model.ToolCallRequest{
					ID:        tc.ID,
					Name:      toolName,
					Arguments: argsMap,
				}

				var toolResult *model.ToolResult
				if handler, ok := a.tools[toolName]; ok {
					toolResult = handler(ctx, toolReq)
				} else {
					toolResult = model.NewToolError(fmt.Errorf("unknown tool: %s", toolName))
				}

				messages = append(messages, &model.Message{
					Role:       model.RoleTool,
					ToolCallID: tc.ID,
					Content:    toolResult.Content,
				})
			}
			continue
		}

		// No tool calls — final response
		result.FinalResponse = assistantMsg.Content
		a.logger.Info("conversation complete", "iterations", iteration)
		return result
	}

	result.Error = fmt.Errorf("max iterations (%d) reached", a.config.MaxIterations)
	return result
}

// RunConversation runs a complete conversation loop with tool calling.
// It returns when the model produces a text response (no tool calls) or max iterations reached.
func (a *AIAgent) RunConversation(ctx context.Context, userMessage string, systemPrompt string) *RunResult {
	// Build initial message list
	var messages []*model.Message
	if systemPrompt != "" {
		messages = append(messages, model.SystemMessage(systemPrompt))
	}
	messages = append(messages, model.UserMessage(userMessage))

	iteration := 0
	result := &RunResult{Messages: messages}

	for iteration < a.config.MaxIterations {
		iteration++
		result.Iterations = iteration

		a.logger.Info("iteration start", "iteration", iteration)

		// Build request
		req := &model.ChatRequest{
			Model:    a.config.Model,
			Messages: messages,
			Tools:    a.config.Tools,
		}

		// Debug: print context before LLM call
		if debugJSON, err := json.MarshalIndent(req, "", "  "); err == nil {
			a.logger.Info(string(debugJSON))
		}

		// Call the LLM
		resp, err := a.client.Chat(ctx, req)
		if err != nil {
			result.Error = err
			a.logger.Error("chat error", "error", err)
			return result
		}

		if len(resp.Choices) == 0 {
			result.Error = fmt.Errorf("no choices returned")
			return result
		}

		choice := resp.Choices[0]
		assistantMsg := choice.Message
		messages = append(messages, assistantMsg)

		// Check for tool calls
		if len(assistantMsg.ToolCalls) > 0 {
			a.logger.Info("tool calls detected", "count", len(assistantMsg.ToolCalls))

			for _, tc := range assistantMsg.ToolCalls {
				toolName := tc.Function.Name
				args := string(tc.Function.Arguments)

				// Parse arguments into a map
				var argsMap map[string]any
				if err := json.Unmarshal([]byte(args), &argsMap); err != nil {
					a.logger.Warn("failed to parse tool arguments", "tool", toolName, "error", err)
					argsMap = nil
				}

				toolReq := &model.ToolCallRequest{
					ID:        tc.ID,
					Name:      toolName,
					Arguments: argsMap,
				}

				var toolResult *model.ToolResult
				if handler, ok := a.tools[toolName]; ok {
					toolResult = handler(ctx, toolReq)
				} else {
					toolResult = model.NewToolError(fmt.Errorf("unknown tool: %s", toolName))
				}

				messages = append(messages, &model.Message{
					Role:       model.RoleTool,
					ToolCallID: tc.ID,
					Content:    toolResult.Content,
				})
			}
			continue
		}

		// No tool calls — final response
		result.FinalResponse = assistantMsg.Content
		a.logger.Info("conversation complete", "iterations", iteration)
		return result
	}

	result.Error = fmt.Errorf("max iterations (%d) reached", a.config.MaxIterations)
	return result
}

// Chat is a simple single-turn chat interface that returns the final text.
func (a *AIAgent) Chat(ctx context.Context, message string) (string, error) {
	result := a.RunConversation(ctx, message, "")
	if result.Error != nil {
		return "", result.Error
	}
	return result.FinalResponse, nil
}

// SyncToolsToConfig populates Config.Tools from the registered tool defs.
// Call this after registering tools and before the first LLM request.
func (a *AIAgent) SyncToolsToConfig() {
	a.config.Tools = nil
	for _, def := range a.toolDefs {
		a.config.Tools = append(a.config.Tools, &model.Tool{
			Type: "function",
			Function: &model.FunctionDef{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  def.InputSchema,
			},
		})
	}
}

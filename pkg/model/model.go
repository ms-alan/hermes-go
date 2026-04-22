package model

import (
	"context"
	"io"
)

// ChatRequest is the input to a chat completion call.
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []*Message    `json:"messages"`
	Tools       []*Tool       `json:"tools,omitempty"`
	ToolChoice  *ToolChoice   `json:"tool_choice,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	Stop        []string      `json:"stop,omitempty"`
}

// ChatResponse is the output from a chat completion call.
type ChatResponse struct {
	ID      string   `json:"id"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice is a single completion choice.
type Choice struct {
	Index        int      `json:"index"`
	Message      *Message `json:"message"`
	FinishReason string   `json:"finish_reason"`
}

// Usage represents token usage statistics.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamChunk is a chunk from a streaming response.
type StreamChunk struct {
	ID    string   `json:"id"`
	Delta Delta    `json:"delta"`
	Usage Usage    `json:"usage,omitempty"`
}

// Delta is the delta content in a streaming chunk.
type Delta struct {
	Content    string `json:"content,omitempty"`
	Role       Role   `json:"role,omitempty"`
	ToolCalls  []*ToolCall `json:"tool_calls,omitempty"`
	ToolPlan   string `json:"tool_plan,omitempty"`
}

// LLMClient is the interface for interacting with an LLM API.
type LLMClient interface {
	// Chat sends a chat completion request and returns the response.
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	// Stream sends a chat completion request with streaming and yields chunks.
	Stream(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error)
	// Close releases resources held by the client.
	Close() error
}

// StreamReader reads streaming chunks from an LLM response.
type StreamReader struct {
	Ch <-chan StreamChunk
}

// Next returns the next chunk, blocking until available or context cancelled.
func (sr *StreamReader) Next(ctx context.Context) (*StreamChunk, error) {
	select {
	case chunk, ok := <-sr.Ch:
		if !ok {
			return nil, io.EOF
		}
		return &chunk, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

package model

import (
	"context"
	"strings"
)

// MiniMaxClient is a MiniMax AI client using the OpenAI-compatible API.
// MiniMax uses the same chat/completions endpoint as OpenAI but at
// https://api.minimaxi.com/anthropic with MiniMax-specific authentication.
type MiniMaxClient struct {
	*OpenAIClient
	model string
}

// NewMiniMaxClient creates a new MiniMax AI client.
// It is functionally identical to OpenAIClient but defaults to the MiniMax
// base URL and uses the MiniMax API key header format.
//
// Supported models: "MiniMax-Text-01", "MiniMax-Reasoning", or any
// OpenAI-compatible model hosted on MiniMax.
func NewMiniMaxClient(apiKey string, model string, opts ...Option) (*MiniMaxClient, error) {
	opts = append(opts,
		WithBaseURL("https://api.minimaxi.com/anthropic"),
		WithAPIKey(apiKey),
		WithModel(model),
	)

	oc, err := NewOpenAIClient(opts...)
	if err != nil {
		return nil, err
	}

	return &MiniMaxClient{
		OpenAIClient: oc,
		model:        model,
	}, nil
}

// Chat implements LLMClient.Chat using the MiniMax API.
func (c *MiniMaxClient) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	return c.OpenAIClient.Chat(ctx, req)
}

// Stream implements LLMClient.Stream using the MiniMax API.
func (c *MiniMaxClient) Stream(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	return c.OpenAIClient.Stream(ctx, req)
}

// Ensure MiniMaxClient implements LLMClient at compile time.
var _ LLMClient = (*MiniMaxClient)(nil)

// detectProvider returns "minimax" if the baseURL matches known MiniMax endpoints,
// "openai" for OpenAI-compatible, and "generic" otherwise.
func detectProvider(baseURL string) string {
	baseURL = strings.ToLower(strings.TrimSpace(baseURL))
	if strings.Contains(baseURL, "minimaxi") || strings.Contains(baseURL, "minimax") {
		return "minimax"
	}
	if strings.Contains(baseURL, "openai") {
		return "openai"
	}
	return "generic"
}

package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- Test helpers ----

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r)
	}))
}

func newTestClient(t *testing.T, server *httptest.Server) *OpenAIClient {
	client, err := NewOpenAIClient(
		WithBaseURL(server.URL),
		WithAPIKey("test-key"),
		WithModel("test-model"),
	)
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}
	return client
}

// ---- NewOpenAIClient ----

func TestNewOpenAIClient(t *testing.T) {
	t.Run("requires base URL", func(t *testing.T) {
		_, err := NewOpenAIClient(WithAPIKey("key"), WithModel("model"))
		if !errors.Is(err, ErrNoBaseURL) {
			t.Errorf("error = %v, want %v", err, ErrNoBaseURL)
		}
	})

	t.Run("requires API key", func(t *testing.T) {
		_, err := NewOpenAIClient(WithBaseURL("https://api.openai.com/v1"))
		if !errors.Is(err, ErrMissingAPIKey) {
			t.Errorf("error = %v, want %v", err, ErrMissingAPIKey)
		}
	})

	t.Run("requires model", func(t *testing.T) {
		_, err := NewOpenAIClient(WithBaseURL("https://api.openai.com/v1"), WithAPIKey("key"))
		if !errors.Is(err, ErrMissingModel) {
			t.Errorf("error = %v, want %v", err, ErrMissingModel)
		}
	})

	t.Run("creates client with all options", func(t *testing.T) {
		client, err := NewOpenAIClient(
			WithBaseURL("https://api.openai.com/v1"),
			WithAPIKey("sk-key"),
			WithModel("gpt-4o"),
			WithTimeout(60),
			WithExtraHeaders(map[string]string{"X-Custom": "header"}),
		)
		if err != nil {
			t.Fatalf("NewOpenAIClient: %v", err)
		}
		if client == nil {
			t.Fatal("client is nil")
		}
	})

	t.Run("trims trailing slash from base URL", func(t *testing.T) {
		_, err := NewOpenAIClient(
			WithBaseURL("https://api.openai.com/v1/"),
			WithAPIKey("key"),
			WithModel("model"),
		)
		if err != nil {
			t.Fatalf("NewOpenAIClient: %v", err)
		}
	})
}

func TestNewOpenAIClientWithLogger(t *testing.T) {
	logger := slog.Default()
	client, err := NewOpenAIClientWithLogger(logger,
		WithBaseURL("https://api.openai.com/v1"),
		WithAPIKey("key"),
		WithModel("model"),
	)
	if err != nil {
		t.Fatalf("NewOpenAIClientWithLogger: %v", err)
	}
	if client == nil {
		t.Fatal("client is nil")
	}
}

// ---- Chat success ----

func TestChat_Success(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization header = %q, want %q", r.Header.Get("Authorization"), "Bearer test-key")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want %q", r.Header.Get("Content-Type"), "application/json")
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}

		resp := ChatResponse{
			ID: "chatcmpl-123",
			Choices: []Choice{
				{
					Index: 0,
					Message: &Message{
						Role:    RoleAssistant,
						Content: "Hello! How can I help you?",
					},
					FinishReason: "stop",
				},
			},
			Usage: Usage{
				PromptTokens:     10,
				CompletionTokens: 8,
				TotalTokens:      18,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	req := &ChatRequest{
		Model:    "test-model",
		Messages: []*Message{UserMessage("Hello")},
	}
	resp, err := client.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Errorf("len(choices) = %d, want 1", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hello! How can I help you?" {
		t.Errorf("content = %q, want %q", resp.Choices[0].Message.Content, "Hello! How can I help you?")
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want %q", resp.Choices[0].FinishReason, "stop")
	}
	if resp.Usage.TotalTokens != 18 {
		t.Errorf("usage.total_tokens = %d, want 18", resp.Usage.TotalTokens)
	}
}

// ---- Chat with tools ----

func TestChat_WithTools(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var receivedReq ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&receivedReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(receivedReq.Tools) != 1 {
			t.Errorf("len(tools) = %d, want 1", len(receivedReq.Tools))
		}

		resp := ChatResponse{
			ID: "chatcmpl-tools",
			Choices: []Choice{
				{
					Index: 0,
					Message: &Message{
						Role:    RoleAssistant,
						Content: "I'll check the weather for you.",
						ToolCalls: []*ToolCall{
							{ID: "call_abc123", Type: "function", Function: &FunctionCall{Name: "get_weather", Arguments: json.RawMessage(`{"location":"New York"}`)}},
						},
					},
					FinishReason: "tool_calls",
				},
			},
			Usage: Usage{PromptTokens: 20, CompletionTokens: 15, TotalTokens: 35},
		}
		json.NewEncoder(w).Encode(resp)
	})
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	tools := []*Tool{
		{
			Type: "function",
			Function: &FunctionDef{
				Name:        "get_weather",
				Description: "Get weather for a location",
				Parameters:   json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}}}`),
			},
		},
	}
	req := &ChatRequest{
		Model:    "test-model",
		Messages: []*Message{UserMessage("What's the weather in New York?")},
		Tools:    tools,
	}
	resp, err := client.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat with tools: %v", err)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Errorf("len(tool_calls) = %d, want 1", len(resp.Choices[0].Message.ToolCalls))
	}
	if resp.Choices[0].Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("tool name = %q, want %q", resp.Choices[0].Message.ToolCalls[0].Function.Name, "get_weather")
	}
}

// ---- Chat error: non-200 status ----

func TestChat_Non200Status(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"Internal server error","type":"server_error"}}`))
	})
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	req := &ChatRequest{Model: "test-model", Messages: []*Message{UserMessage("Hi")}}
	_, err := client.Chat(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.Code != 500 {
			t.Errorf("api error code = %d, want 500", apiErr.Code)
		}
	}
}

// ---- Chat error: connection failure ----

func TestChat_ConnectionError(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Never respond - causes connection error via httptest
	})
	serverURL := server.URL
	server.Close() // Close immediately to cause connection error

	client, err := NewOpenAIClient(
		WithBaseURL(serverURL),
		WithAPIKey("test-key"),
		WithModel("test-model"),
	)
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}

	req := &ChatRequest{Model: "test-model", Messages: []*Message{UserMessage("Hi")}}
	_, err = client.Chat(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for connection failure")
	}
}

// ---- Chat error: invalid JSON response ----

func TestChat_InvalidResponseJSON(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not valid json`))
	})
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	req := &ChatRequest{Model: "test-model", Messages: []*Message{UserMessage("Hi")}}
	_, err := client.Chat(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

// ---- Chat error: empty choices ----

func TestChat_EmptyChoices(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := ChatResponse{
			ID:      "chatcmpl-empty",
			Choices: []Choice{},
			Usage:   Usage{PromptTokens: 5, CompletionTokens: 0, TotalTokens: 5},
		}
		json.NewEncoder(w).Encode(resp)
	})
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	req := &ChatRequest{Model: "test-model", Messages: []*Message{UserMessage("Hi")}}
	resp, err := client.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Choices) != 0 {
		t.Errorf("len(choices) = %d, want 0", len(resp.Choices))
	}
}

// ---- Stream success ----

func TestStream_Success(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		chunks := []string{
			`data: {"id":"1","delta":{"content":"Hello"},"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6},"finish_reason":null}`,
			`data: {"id":"2","delta":{"content":" world"},"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7},"finish_reason":null}`,
			`data: {"id":"3","delta":{"content":""},"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8},"finish_reason":"stop"}`,
			`data: [DONE]`,
		}
		for _, chunk := range chunks {
			w.Write([]byte(chunk + "\n"))
			w.(http.Flusher).Flush()
		}
	})
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	req := &ChatRequest{Model: "test-model", Messages: []*Message{UserMessage("Hi")}}
	ch, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var contents []string
	for chunk := range ch {
		contents = append(contents, chunk.Delta.Content)
	}

	full := strings.Join(contents, "")
	if full != "Hello world" {
		t.Errorf("streamed content = %q, want %q", full, "Hello world")
	}
}

// ---- Stream with tool calls ----

func TestStream_WithToolCalls(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`data: {"id":"tc1","delta":{"content":"Let me check","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_time","arguments":"{}"}}]},"usage":null}`,
			`data: [DONE]`,
		}
		for _, chunk := range chunks {
			w.Write([]byte(chunk + "\n"))
			w.(http.Flusher).Flush()
		}
	})
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	req := &ChatRequest{Model: "test-model", Messages: []*Message{UserMessage("What time is it?")}}
	ch, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var hasToolCall bool
	for chunk := range ch {
		if len(chunk.Delta.ToolCalls) > 0 {
			hasToolCall = true
			if chunk.Delta.ToolCalls[0].Function.Name != "get_time" {
				t.Errorf("tool name = %q, want %q", chunk.Delta.ToolCalls[0].Function.Name, "get_time")
			}
		}
	}
	if !hasToolCall {
		t.Error("expected tool call in stream")
	}
}

// ---- Stream error: non-200 status ----

func TestStream_Non200Status(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("service unavailable"))
	})
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	req := &ChatRequest{Model: "test-model", Messages: []*Message{UserMessage("Hi")}}
	ch, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}

	var gotErr bool
	for chunk := range ch {
		if strings.Contains(chunk.Delta.Content, "error") {
			gotErr = true
		}
	}
	if !gotErr {
		t.Error("expected error content in stream for non-200")
	}
}

// ---- Stream context cancellation ----

func TestStream_Cancel(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"id":"1","delta":{"content":"slow"},"usage":null}` + "\n"))
		w.(http.Flusher).Flush()

		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			return
		}
	})
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	req := &ChatRequest{Model: "test-model", Messages: []*Message{UserMessage("Hi")}}
	ch, err := client.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	cancel()

	for range ch {
	}
}

// ---- Stream error: connection failure ----

func TestStream_ConnectionError(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
	})
	serverURL := server.URL
	server.Close()

	client, err := NewOpenAIClient(
		WithBaseURL(serverURL),
		WithAPIKey("test-key"),
		WithModel("test-model"),
	)
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}

	req := &ChatRequest{Model: "test-model", Messages: []*Message{UserMessage("Hi")}}
	ch, err := client.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var gotErr bool
	for chunk := range ch {
		if strings.Contains(chunk.Delta.Content, "error") {
			gotErr = true
		}
	}
	if !gotErr {
		t.Error("expected error in stream channel for connection failure")
	}
}

// ---- Close ----

func TestClose(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	t.Cleanup(server.Close)
	client := newTestClient(t, server)
	err := client.Close()
	if err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ---- OpenAIClient implements LLMClient ----

func TestOpenAIClientImplementsLLMClient(t *testing.T) {
	var _ LLMClient = (*OpenAIClient)(nil)
}

// ---- EventStreamReader ----

func TestEventStreamReader(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data: first chunk\n\ndata: second chunk\n\ndata: [DONE]\n\n"))
	}
	server := newTestServer(t, handler)
	t.Cleanup(server.Close)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	handler(resp, req)

	reader := NewEventStreamReader(resp.Body)
	line1, err := reader.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine 1: %v", err)
	}
	if line1 != "data: first chunk" {
		t.Errorf("line1 = %q, want %q", line1, "data: first chunk")
	}

	line2, err := reader.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine 2: %v", err)
	}
	if line2 != "data: second chunk" {
		t.Errorf("line2 = %q, want %q", line2, "data: second chunk")
	}

	_, err = reader.ReadLine()
	if err != io.EOF {
		t.Errorf("ReadLine EOF: got %v, want %v", err, io.EOF)
	}
}

// ---- Message helpers ----

func TestMessageHelpers(t *testing.T) {
	t.Run("UserMessage", func(t *testing.T) {
		msg := UserMessage("Hello")
		if msg.Role != RoleUser {
			t.Errorf("Role = %s, want %s", msg.Role, RoleUser)
		}
		if msg.Content != "Hello" {
			t.Errorf("Content = %q, want %q", msg.Content, "Hello")
		}
	})

	t.Run("SystemMessage", func(t *testing.T) {
		msg := SystemMessage("You are helpful")
		if msg.Role != RoleSystem {
			t.Errorf("Role = %s, want %s", msg.Role, RoleSystem)
		}
	})

	t.Run("ToolMessage", func(t *testing.T) {
		msg := ToolMessage("call_123", "42 degrees")
		if msg.Role != RoleTool {
			t.Errorf("Role = %s, want %s", msg.Role, RoleTool)
		}
		if msg.ToolCallID != "call_123" {
			t.Errorf("ToolCallID = %q, want %q", msg.ToolCallID, "call_123")
		}
		if msg.Content != "42 degrees" {
			t.Errorf("Content = %q, want %q", msg.Content, "42 degrees")
		}
	})

	t.Run("AssistantMessage", func(t *testing.T) {
		msg := &Message{Role: RoleAssistant, Content: "I'll help", ToolPlan: "thinking"}
		asst := msg.AssistantMessage()
		if asst.Role != RoleAssistant {
			t.Errorf("Role = %s, want %s", asst.Role, RoleAssistant)
		}
		if asst.ToolPlan != "thinking" {
			t.Errorf("ToolPlan = %q, want %q", asst.ToolPlan, "thinking")
		}
	})
}

// ---- ToolCall helpers ----

func TestToolCallHelpers(t *testing.T) {
	tc := &ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: &FunctionCall{
			Name:      "get_weather",
			Arguments: json.RawMessage(`{"location":"NYC"}`),
		},
	}

	args := make(map[string]string)
	err := tc.ParseArguments(&args)
	if err != nil {
		t.Fatalf("ParseArguments: %v", err)
	}
	if args["location"] != "NYC" {
		t.Errorf("location = %q, want %q", args["location"], "NYC")
	}

	raw := tc.GetArguments()
	if raw != `{"location":"NYC"}` {
		t.Errorf("GetArguments = %q, want %q", raw, `{"location":"NYC"}`)
	}
}

// ---- StreamReader.Next ----

func TestStreamReader_Next(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`data: {"id":"1","delta":{"content":"test"},"usage":null}` + "\n\n"))
	})
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	ch, err := client.Stream(context.Background(), &ChatRequest{Model: "m", Messages: []*Message{UserMessage("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	sr := &StreamReader{Ch: ch}
	ctx := context.Background()
	chunk, err := sr.Next(ctx)
	if err != nil {
		t.Fatalf("StreamReader.Next: %v", err)
	}
	if chunk.Delta.Content != "test" {
		t.Errorf("content = %q, want %q", chunk.Delta.Content, "test")
	}

	_, err = sr.Next(ctx)
	if err != io.EOF {
		t.Errorf("second Next: got %v, want EOF", err)
	}
}

func TestStreamReader_Next_ContextCancel(t *testing.T) {
	ch := make(chan StreamChunk, 1)
	sr := &StreamReader{Ch: ch}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := sr.Next(ctx)
	if err != context.Canceled {
		t.Errorf("Next with cancelled context: got %v, want %v", err, context.Canceled)
	}
}

// ---- Error types ----

func TestAPIError(t *testing.T) {
	err := &APIError{Code: 429, Message: "rate limited", Type: "rate_limit_error"}
	if err.Error() != "api error [rate_limit_error]: rate limited" {
		t.Errorf("Error() = %q", err.Error())
	}

	err2 := &APIError{Message: "generic error"}
	if err2.Error() != "api error: generic error" {
		t.Errorf("Error() = %q", err2.Error())
	}
}

func TestRequestError(t *testing.T) {
	baseErr := errors.New("connection refused")
	err := &RequestError{StatusCode: 503, Message: "service unavailable", Raw: baseErr}
	if err.Error() != "request failed (status 503): service unavailable" {
		t.Errorf("Error() = %q", err.Error())
	}
	if !errors.Is(err, baseErr) {
		t.Error("Unwrap should return base error")
	}
}

func TestErrUnexpectedStatus(t *testing.T) {
	err := ErrUnexpectedStatus(404, []byte(`{"error":"not found"}`))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Error("should be an APIError")
	}
	if apiErr.Code != 404 {
		t.Errorf("Code = %d, want 404", apiErr.Code)
	}
}

// ---- ChatRequest marshaling ----

func TestChatRequestJSON(t *testing.T) {
	req := &ChatRequest{
		Model:       "gpt-4",
		Temperature: 0.7,
		MaxTokens:   100,
		Messages: []*Message{
			{Role: RoleSystem, Content: "You are a helpful assistant"},
			{Role: RoleUser, Content: "Hello"},
		},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"temperature":0.7`) {
		t.Errorf("JSON = %s, should contain temperature", string(data))
	}
	if !strings.Contains(string(data), `"max_tokens":100`) {
		t.Errorf("JSON = %s, should contain max_tokens", string(data))
	}
}

// ---- Roles ----

func TestRoles(t *testing.T) {
	if RoleSystem != "system" {
		t.Errorf("RoleSystem = %q, want %q", RoleSystem, "system")
	}
	if RoleUser != "user" {
		t.Errorf("RoleUser = %q, want %q", RoleUser, "user")
	}
	if RoleAssistant != "assistant" {
		t.Errorf("RoleAssistant = %q, want %q", RoleAssistant, "assistant")
	}
	if RoleTool != "tool" {
		t.Errorf("RoleTool = %q, want %q", RoleTool, "tool")
	}
}

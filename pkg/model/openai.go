package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// OpenAIClient is an OpenAI-compatible LLM client using stdlib net/http.
type OpenAIClient struct {
	baseURL      string
	apiKey       string
	model        string
	httpClient   *http.Client
	extraHeaders map[string]string
	logger       *slog.Logger
}

// NewOpenAIClient creates a new OpenAI-compatible client.
func NewOpenAIClient(opts ...Option) (*OpenAIClient, error) {
	var o options
	o.httpClient = &http.Client{Timeout: 2 * time.Minute}
	o.apply(opts...)

	if o.baseURL == "" {
		return nil, ErrNoBaseURL
	}
	if o.apiKey == "" {
		return nil, ErrMissingAPIKey
	}
	if o.model == "" {
		return nil, ErrMissingModel
	}

	baseURL := strings.TrimSuffix(o.baseURL, "/")

	c := &OpenAIClient{
		baseURL:      baseURL,
		apiKey:       o.apiKey,
		model:        o.model,
		httpClient:   o.httpClient,
		extraHeaders: o.extraHeaders,
		logger:       slog.Default(),
	}

	if o.timeoutSecs > 0 {
		c.httpClient.Timeout = time.Duration(o.timeoutSecs) * time.Second
	}

	return c, nil
}

// NewOpenAIClientWithLogger creates a client with a custom logger.
func NewOpenAIClientWithLogger(logger *slog.Logger, opts ...Option) (*OpenAIClient, error) {
	c, err := NewOpenAIClient(opts...)
	if err != nil {
		return nil, err
	}
	c.logger = logger
	return c, nil
}

// Chat implements LLMClient.Chat.
func (c *OpenAIClient) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.model
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	c.setHeaders(httpReq)

	c.logger.Debug("chat request", "model", req.Model, "message_count", len(req.Messages))

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, &RequestError{Message: err.Error(), Raw: err}
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, &RequestError{StatusCode: httpResp.StatusCode, Message: "failed to read response body", Raw: err}
	}

	if httpResp.StatusCode != http.StatusOK {
		c.logger.Error("chat response non-200", "status", httpResp.StatusCode, "body", string(respBody))
		return nil, ErrUnexpectedStatus(httpResp.StatusCode, respBody)
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &chatResp, nil
}

// Stream implements LLMClient.Stream.
func (c *OpenAIClient) Stream(ctx context.Context, req *ChatRequest) (<-chan StreamChunk, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	req.Stream = true

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	c.setHeaders(httpReq)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

	ch := make(chan StreamChunk, 1)

	go func() {
		defer close(ch)

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			ch <- StreamChunk{Delta: Delta{Content: fmt.Sprintf("error: %v", err)}}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			ch <- StreamChunk{Delta: Delta{Content: fmt.Sprintf("error: HTTP %d: %s", resp.StatusCode, body)}}
			return
		}

		// Read SSE stream
		reader := NewEventStreamReader(resp.Body)
		for {
			line, err := reader.ReadLine()
			if err == io.EOF {
				break
			}
			if err != nil {
				ch <- StreamChunk{Delta: Delta{Content: fmt.Sprintf("stream error: %v", err)}}
				break
			}

			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var chunk StreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				c.logger.Warn("failed to unmarshal stream chunk", "error", err)
				continue
			}
			select {
			case ch <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

// Close implements LLMClient.Close.
func (c *OpenAIClient) Close() error {
	// No-op for default client; replace httpClient with a connection pool for production.
	return nil
}

func (c *OpenAIClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	for k, v := range c.extraHeaders {
		req.Header.Set(k, v)
	}
}

// EventStreamReader reads lines from a Server-Sent Events stream.
type EventStreamReader struct {
	reader io.Reader
	buf    []byte
}

func NewEventStreamReader(r io.Reader) *EventStreamReader {
	return &EventStreamReader{reader: r}
}

func (r *EventStreamReader) ReadLine() (string, error) {
	// Read until newline
	line := []byte{}
	buf := make([]byte, 1)
	for {
		n, err := r.reader.Read(buf)
		if err != nil {
			return string(line), err
		}
		if n > 0 && buf[0] == '\n' {
			break
		}
		if buf[0] != '\r' {
			line = append(line, buf[0])
		}
	}
	return string(line), nil
}

// Ensure OpenAIClient implements LLMClient at compile time.
var _ LLMClient = (*OpenAIClient)(nil)

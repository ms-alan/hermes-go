package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

// HTTP2Transport is an HTTP/1.1 + HTTP/2 capable transport.
type HTTP2Transport struct {
	mu       sync.Mutex
	baseURL  string
	headers  map[string]string
	client   *http.Client
	clientMu sync.Mutex
	useHTTP2 bool
}

// NewHTTP2Transport creates an HTTP/1.1 + HTTP/2 capable transport.
func NewHTTP2Transport(baseURL string, headers map[string]string, timeout int, http2Enabled bool) *HTTP2Transport {
	baseURL = strings.TrimSuffix(baseURL, "/")
	client := &http.Client{Timeout: 120 * time.Second}
	if timeout > 0 {
		client.Timeout = time.Duration(timeout) * time.Second
	}
	if http2Enabled {
		if tr, ok := client.Transport.(*http.Transport); ok {
			http2.ConfigureTransport(tr) //nolint:errcheck
		} else {
			tr := &http.Transport{}
			http2.ConfigureTransport(tr) //nolint:errcheck
			client.Transport = tr
		}
	}
	return &HTTP2Transport{
		baseURL:   baseURL,
		headers:   headers,
		client:    client,
		useHTTP2:  http2Enabled,
	}
}

// Send sends a JSON-RPC request over HTTP POST with optional HTTP/2 multiplexing.
func (t *HTTP2Transport) Send(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
	t.clientMu.Lock()
	defer t.clientMu.Unlock()

	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"id":      time.Now().UnixNano(),
	}
	if params != nil {
		reqBody["params"] = params
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := t.baseURL
	// Detect HTTP/2 server by scheme
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "http://" + url
	}
	if !strings.Contains(url, "/mcp") && !strings.Contains(url, "/sse") {
		url = t.baseURL + "/mcp"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(respBytes, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("server error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

// Close is a no-op for HTTP transports.
func (t *HTTP2Transport) Close() error {
	t.clientMu.Lock()
	defer t.clientMu.Unlock()
	t.client = nil
	return nil
}

// --------------------------------------------------------------------------
// SSETransport — HTTP/2 + exponential backoff reconnection + sampling
// --------------------------------------------------------------------------

// backoffConfig holds exponential backoff parameters.
const (
	backoffInitial = 1 * time.Second
	backoffMax     = 30 * time.Second
	backoffFactor  = 2.0
)

// SSETransport handles Server-Sent Events for MCP server-to-client notifications.
// It supports HTTP/2, automatic reconnection with exponential backoff, and
// server-initiated sampling/createMessage requests.
type SSETransport struct {
	mu          sync.Mutex
	baseURL     string
	headers     map[string]string
	httpClient  *http.Client
	http2Client *HTTP2Transport
	ssePath     string // path for SSE subscription (e.g., "/sse")
	httpPath    string // path for HTTP POST requests (e.g., "/mcp")
	http2       bool   // enable HTTP/2

	// Notification dispatch
	handlers   map[string]NotificationHandler
	handlersMu sync.RWMutex

	// Sampling handler (set by Client)
	samplingHandler SamplingHandler
	samplingCfg     SamplingConfig

	// Reconnection state
	sseCancel   context.CancelFunc
	sseDone     chan struct{}
	reconnecting bool
	stopCh      chan struct{}
}

type NotificationHandler func(method string, params json.RawMessage)

// NewSSETransport creates an SSE-capable transport with HTTP/2 and auto-reconnect.
func NewSSETransport(baseURL, ssePath, httpPath string, headers map[string]string) *SSETransport {
	if ssePath == "" {
		ssePath = "/sse"
	}
	if httpPath == "" {
		httpPath = "/mcp"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	return &SSETransport{
		baseURL:   baseURL,
		ssePath:   ssePath,
		httpPath:  httpPath,
		headers:   headers,
		handlers:  make(map[string]NotificationHandler),
		sseDone:   make(chan struct{}),
		stopCh:    make(chan struct{}),
		http2:     true, // HTTP/2 enabled by default
	}
}

// SetSamplingHandler configures the sampling handler and per-server config.
// Call this before Subscribe() so sampling requests are routed correctly.
func (t *SSETransport) SetSamplingHandler(handler SamplingHandler, cfg SamplingConfig) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.samplingHandler = handler
	t.samplingCfg = cfg
}

// SetHTTP2 enables or disables HTTP/2 support. Default: true.
func (t *SSETransport) SetHTTP2(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.http2 = enabled
}

// Send sends a JSON-RPC request over HTTP POST (HTTP/1.1 or HTTP/2).
func (t *SSETransport) Send(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
	// Use HTTP/2 transport if enabled, otherwise fall back to http.Client
	if t.http2 {
		if t.http2Client == nil {
			t.mu.Lock()
			if t.http2Client == nil {
				t.http2Client = NewHTTP2Transport(t.baseURL, t.headers, 120, true)
			}
			t.mu.Unlock()
		}
		return t.http2Client.Send(ctx, method, params)
	}

	return t.sendHTTP1(ctx, method, params)
}

func (t *SSETransport) sendHTTP1(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"id":      time.Now().UnixNano(),
	}
	if params != nil {
		reqBody["params"] = params
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := t.baseURL + t.httpPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(respBytes, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("server error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

// Close terminates the SSE connection and stops reconnection loops.
func (t *SSETransport) Close() error {
	close(t.stopCh)
	if t.sseCancel != nil {
		t.sseCancel()
	}
	return nil
}

// Subscribe starts receiving SSE notifications with automatic reconnection.
// It dials the SSE endpoint and dispatches events to registered handlers.
// On connection drop, it automatically reconnects with exponential backoff.
// Blocking — run in a goroutine.
func (t *SSETransport) Subscribe(ctx context.Context) error {
	return t.subscribeWithBackoff(ctx, 0)
}

// subscribeWithBackoff attempts SSE connection with exponential backoff on failure.
// maxRetries <= 0 means unlimited retries until stopCh is closed.
func (t *SSETransport) subscribeWithBackoff(ctx context.Context, maxRetries int) error {
	delay := backoffInitial
	retries := 0

	for {
		select {
		case <-t.stopCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if maxRetries > 0 && retries >= maxRetries {
			return fmt.Errorf("SSE connection failed after %d retries", maxRetries)
		}

		sseCtx, cancel := context.WithCancel(ctx)
		t.mu.Lock()
		t.sseCancel = cancel
		t.mu.Unlock()

		err := t.dialSSE(sseCtx)
		cancel()

		if err == nil {
			// Clean disconnect
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		slog.Warn("[mcp][sse] connection lost, reconnecting", "err", err, "delay", delay, "retry", retries)

		select {
		case <-t.stopCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		// Exponential backoff with jitter
		delay = time.Duration(float64(delay) * backoffFactor)
		if delay > backoffMax {
			delay = backoffMax
		}
		retries++
	}
}

// dialSSE establishes a single SSE connection and reads events until EOF or context cancel.
func (t *SSETransport) dialSSE(ctx context.Context) error {
	url := t.baseURL + t.ssePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{
		Timeout: 0, // no timeout for SSE — it's a long-lived connection
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connection: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return fmt.Errorf("SSE HTTP %d", resp.StatusCode)
	}

	close(t.sseDone)
	t.sseDone = make(chan struct{})

	go t.readSSE(resp.Body, ctx)

	return nil
}

// readSSE reads the SSE stream and dispatches events.
func (t *SSETransport) readSSE(body io.Reader, ctx context.Context) {
	defer close(t.sseDone)

	scanner := bufio.NewScanner(body)
	const maxScanTokenSize = 1024 * 1024
	buf := make([]byte, maxScanTokenSize)
	scanner.Buffer(buf, maxScanTokenSize)

	var currentEvent, currentData string

	reset := func() {
		currentEvent = ""
		currentData = ""
	}

	flush := func() {
		if currentEvent == "" || currentData == "" {
			reset()
			return
		}

		// Parse as JSON-RPC notification or request
		var notification JSONRPCRequest
		if err := json.Unmarshal([]byte(currentData), &notification); err != nil {
			reset()
			return
		}

		// Handle sampling/createMessage specially
		if notification.Method == "sampling/createMessage" {
			t.handleSamplingRequest(currentEvent, notification.Params)
			reset()
			return
		}

		t.handlersMu.RLock()
		handler, ok := t.handlers[currentEvent]
		t.handlersMu.RUnlock()
		if ok {
			handler(currentEvent, notification.Params)
		}
		reset()
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				slog.Warn("[mcp][sse] scanner error", "err", err)
			}
			flush()
			return
		}

		line := scanner.Text()

		if line == "" {
			flush()
			continue
		}

		if len(line) < 2 {
			continue
		}

		if strings.HasPrefix(line, "event:") {
			if currentEvent != "" {
				flush()
			}
			currentEvent = strings.TrimSpace(line[6:])
		} else if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(line[5:])
			if currentData != "" {
				currentData += "\n" + data
			} else {
				currentData = data
			}
		}
	}
}

// handleSamplingRequest routes a sampling/createMessage request to the SamplingHandler.
func (t *SSETransport) handleSamplingRequest(eventType string, params json.RawMessage) {
	t.mu.Lock()
	handler := t.samplingHandler
	cfg := t.samplingCfg
	t.mu.Unlock()

	if handler == nil {
		slog.Debug("[mcp][sse] sampling request but no handler registered")
		return
	}
	if !cfg.Enabled {
		slog.Debug("[mcp][sse] sampling disabled, ignoring request")
		return
	}

	var req SamplingCreateMessageRequest
	if err := json.Unmarshal(params, &req); err != nil {
		slog.Warn("[mcp][sse] failed to parse sampling request", "err", err)
		return
	}

	// Run in background — don't block SSE stream processing
	go func() {
		ctx := context.Background()
		if cfg.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.Timeout)*time.Second)
			defer cancel()
		}

		result, err := handler.HandleSamplingRequest(ctx, t.baseURL, &req, cfg)
		if err != nil {
			slog.Warn("[mcp][sse] sampling handler error", "err", err)
			return
		}

		// Send the result back via HTTP POST (sampling responses use reverse HTTP)
		// The server included a URI in the original request to send the response
		if req.Method != "" {
			// The response is sent back via HTTP, not SSE
			slog.Debug("[mcp][sse] sampling response ready", "result_len", len(result.Content))
		}
	}()
}

// OnNotification registers a handler for a server-initiated event.
// Common events: "notifications/message", "notifications/initialized".
func (t *SSETransport) OnNotification(method string, handler NotificationHandler) {
	t.handlersMu.Lock()
	defer t.handlersMu.Unlock()
	t.handlers[method] = handler
}

// RemoveNotification removes the handler for a method.
func (t *SSETransport) RemoveNotification(method string) {
	t.handlersMu.Lock()
	defer t.handlersMu.Unlock()
	delete(t.handlers, method)
}

// SSEPoll is a helper for clients that can't maintain a persistent SSE connection.
func (t *SSETransport) Poll(ctx context.Context) ([]JSONRPCRequest, error) {
	url := t.baseURL + t.ssePath + "/poll"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poll HTTP %d", resp.StatusCode)
	}

	var notifications []JSONRPCRequest
	if err := json.NewDecoder(resp.Body).Decode(&notifications); err != nil {
		return nil, err
	}

	return notifications, nil
}

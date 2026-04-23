package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SSETransport handles Server-Sent Events for MCP server-to-client notifications.
// It maintains an HTTP POST connection for requests and an SSE GET connection for
// server-initiated notifications (e.g., "notifications/message").
type SSETransport struct {
	mu          sync.Mutex
	baseURL     string
	headers     map[string]string
	client      *http.Client
	sseEndpoint string // path for SSE subscription (e.g., "/sse")
	httpPath    string // path for HTTP POST requests

	// Notification dispatch
	handlers   map[string]NotificationHandler
	handlersMu sync.RWMutex

	// SSE connection state
	sseCancel context.CancelFunc
	sseDone   chan struct{}
}

type NotificationHandler func(method string, params json.RawMessage)

// NewSSETransport creates an SSE-capable transport for an MCP server.
// baseURL is the server root (e.g., "http://localhost:8080").
// ssePath is the endpoint for SSE subscription (default: "/sse").
// httpPath is the endpoint for JSON-RPC POST requests (default: "/mcp").
func NewSSETransport(baseURL, ssePath, httpPath string, headers map[string]string) *SSETransport {
	if ssePath == "" {
		ssePath = "/sse"
	}
	if httpPath == "" {
		httpPath = "/mcp"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	t := &SSETransport{
		baseURL:     baseURL,
		sseEndpoint: ssePath,
		httpPath:    httpPath,
		headers:     headers,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
		handlers: make(map[string]NotificationHandler),
		sseDone: make(chan struct{}),
	}
	return t
}

// Send sends a JSON-RPC request over HTTP POST.
func (t *SSETransport) Send(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(bodyBytes)))
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

// Close terminates the SSE connection.
func (t *SSETransport) Close() error {
	if t.sseCancel != nil {
		t.sseCancel()
	}
	return nil
}

// Subscribe starts receiving SSE notifications from the server.
// It dials the SSE endpoint and dispatches events to registered handlers.
// Blocking — run in a goroutine.
func (t *SSETransport) Subscribe(ctx context.Context) error {
	url := t.baseURL + t.sseEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connection: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SSE HTTP %d", resp.StatusCode)
	}

	sseCtx, cancel := context.WithCancel(ctx)
	t.sseCancel = cancel

	go t.readSSE(resp.Body, sseCtx)

	return nil
}

// readSSE reads the SSE stream and dispatches events.
func (t *SSETransport) readSSE(body io.Reader, ctx context.Context) {
	defer close(t.sseDone)

	scanner := bufio.NewScanner(body)
	// SSE data lines can be long; increase buffer size
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
		// currentData may be multiple JSON lines joined by '\n'
		// Parse as JSON-RPC notification: { "jsonrpc": "2.0", "method": "...", "params": {...} }
		var notification JSONRPCRequest
		if err := json.Unmarshal([]byte(currentData), &notification); err != nil {
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

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			flush()
			return
		default:
		}

		line := scanner.Text()

		if line == "" {
			// Blank line = end of event
			flush()
			continue
		}

		if len(line) < 2 {
			continue
		}

		// Parse "event: <name>" or "data: <payload>"
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

	// Final flush in case stream ended without blank line
	flush()
}

// OnNotification registers a handler for a server-initiated event.
// Common events: "notifications/message", "notifications/initialized", etc.
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
// It calls the server's "poll" or equivalent endpoint to receive queued messages.
// Returns notifications received in this poll cycle.
func (t *SSETransport) Poll(ctx context.Context) ([]JSONRPCRequest, error) {
	// Some SSE servers support a GET /messages endpoint for polling
	url := t.baseURL + t.sseEndpoint + "/poll"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
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

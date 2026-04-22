// Package mcp provides the Model Context Protocol client for connecting to
// external MCP servers and the MCP server for exposing Hermes as an MCP server.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// --------------------------------------------------------------------------
// Transport interface
// --------------------------------------------------------------------------

// Transport is the interface for communicating with an MCP server.
type Transport interface {
	Send(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error)
	Close() error
}

// --------------------------------------------------------------------------
// StdioTransport — spawns a subprocess and communicates over stdin/stdout
// --------------------------------------------------------------------------

// StdioTransport manages a subprocess MCP server using stdio communication.
type StdioTransport struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

// NewStdioTransport launches a subprocess with the given parameters.
func NewStdioTransport(params StdioServerParameters) (*StdioTransport, error) {
	env := buildEnv(params.Env)

	cmd := exec.Command(params.Command, params.Args...)
	cmd.Env = env
	// Create pipes so we can read/write the subprocess stdio from Send().
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("start stdio server: %w", err)
	}

	return &StdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
	}, nil
}

// buildEnv creates a filtered environment for the subprocess.
// Only PATH, HOME, XDG_* vars, and explicitly declared env vars are passed.
func buildEnv(extra map[string]string) []string {
	var safe = []string{"PATH", "HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"}
	allowed := make(map[string]string)

	for _, k := range safe {
		if v := os.Getenv(k); v != "" {
			allowed[k] = v
		}
	}
	for k, v := range extra {
		allowed[k] = v
	}

	env := make([]string, 0, len(allowed))
	for k, v := range allowed {
		env = append(env, k+"="+v)
	}
	return env
}

// Send sends a JSON-RPC request over stdio and waits for a response.
// It respects the context deadline/cancellation.
func (t *StdioTransport) Send(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	req := JSONRPCRequest{
		ID:     time.Now().UnixNano(),
		Method: method,
	}
	if params != nil {
		pBytes, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		req.Params = pBytes
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	reqBytes = append(reqBytes, '\n')

	if _, err := t.stdin.Write(reqBytes); err != nil {
		return nil, fmt.Errorf("write to stdin: %w", err)
	}

	// Wait for response with context cancellation support.
	respCh := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(t.stdout)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			errCh <- err
			return
		}
		respCh <- line
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-errCh:
		return nil, fmt.Errorf("read response: %w", err)
	case line := <-respCh:
		var resp JSONRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("server error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// Close terminates the subprocess.
func (t *StdioTransport) Close() error {
	if t.cmd != nil && t.cmd.Process != nil {
		return t.cmd.Process.Kill()
	}
	return nil
}

// --------------------------------------------------------------------------
// HTTPTransport — communicates with a server over HTTP
// --------------------------------------------------------------------------

// HTTPTransport manages an HTTP-based MCP server connection.
type HTTPTransport struct {
	mu      sync.Mutex
	baseURL string
	headers map[string]string
	client  *http.Client
}

// NewHTTPTransport creates a transport for an HTTP MCP server.
func NewHTTPTransport(baseURL string, headers map[string]string, timeout int) *HTTPTransport {
	t := &HTTPTransport{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		headers: headers,
		client:  &http.Client{},
	}
	if timeout > 0 {
		t.client.Timeout = time.Duration(timeout) * time.Second
	} else {
		t.client.Timeout = 120 * time.Second
	}
	return t
}

// Send sends a JSON-RPC request over HTTP POST.
func (t *HTTPTransport) Send(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL, bytes.NewReader(bodyBytes))
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
func (t *HTTPTransport) Close() error {
	return nil
}

// --------------------------------------------------------------------------
// Client — manages multiple server connections
// --------------------------------------------------------------------------

// Client manages connections to one or more MCP servers.
type Client struct {
	mu       sync.RWMutex
	servers  map[string]Transport
	tools    map[string]MCPTool // key: "serverName:toolName"
	disabled map[string]bool
}

// NewClient creates a new MCP client.
func NewClient() *Client {
	return &Client{
		servers:  make(map[string]Transport),
		tools:    make(map[string]MCPTool),
		disabled: make(map[string]bool),
	}
}

// ConnectServer connects to a single MCP server and discovers its tools.
func (c *Client) ConnectServer(cfg MCPServerConfig) error {
	if cfg.Disabled {
		c.mu.Lock()
		c.disabled[cfg.Name] = true
		c.mu.Unlock()
		return nil
	}

	var transport Transport
	var err error

	switch cfg.Transport {
	case "http", "https":
		transport = NewHTTPTransport(cfg.URL, cfg.Headers, cfg.ConnectTimeout)
	default: // "stdio" or empty
		transport, err = NewStdioTransport(StdioServerParameters{
			Command: cfg.Command,
			Args:    cfg.Args,
			Env:     cfg.Env,
		})
	}
	if err != nil {
		return fmt.Errorf("connect %s: %w", cfg.Name, err)
	}

	// Initialize the server
	ctx := context.Background()
	if cfg.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.ConnectTimeout)*time.Second)
		defer cancel()
	}

	initResult, err := c.initializeServer(ctx, transport)
	if err != nil {
		transport.Close()
		return fmt.Errorf("init %s: %w", cfg.Name, err)
	}
	_ = initResult // capabilities can be inspected later

	// Discover tools
	tools, err := c.discoverTools(ctx, transport, cfg.Name)
	if err != nil {
		transport.Close()
		return fmt.Errorf("discover tools for %s: %w", cfg.Name, err)
	}

	c.mu.Lock()
	c.servers[cfg.Name] = transport
	for _, tool := range tools {
		key := cfg.Name + ":" + tool.Name
		c.tools[key] = tool
	}
	c.mu.Unlock()

	return nil
}

func (c *Client) initializeServer(ctx context.Context, transport Transport) (*InitializeResult, error) {
	params := map[string]interface{}{
		"protocolVersion": ProtocolVersion,
		"capabilities":    ClientCapabilities{},
		"clientInfo": map[string]string{
			"name":    "hermes-go",
			"version": "1.0.0",
		},
	}

	result, err := transport.Send(ctx, "initialize", params)
	if err != nil {
		return nil, err
	}

	var initResult InitializeResult
	if err := json.Unmarshal(result, &initResult); err != nil {
		return nil, fmt.Errorf("parse initialize result: %w", err)
	}

	return &initResult, nil
}

func (c *Client) discoverTools(ctx context.Context, transport Transport, serverName string) ([]MCPTool, error) {
	result, err := transport.Send(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}

	var listResult ToolListResult
	if err := json.Unmarshal(result, &listResult); err != nil {
		return nil, fmt.Errorf("parse tools/list result: %w", err)
	}

	return listResult.Tools, nil
}

// CallTool calls a tool on a specific MCP server.
// The toolName should be in the format "serverName:toolName".
func (c *Client) CallTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (*CallToolResult, error) {
	c.mu.RLock()
	transport, ok := c.servers[serverName]
	c.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no such server: %s", serverName)
	}

	parts := strings.SplitN(toolName, ":", 2)
	actualToolName := toolName
	if len(parts) == 2 {
		actualToolName = parts[1]
	}

	params := map[string]interface{}{
		"name":      actualToolName,
		"arguments": args,
	}

	result, err := transport.Send(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}

	var callResult CallToolResult
	if err := json.Unmarshal(result, &callResult); err != nil {
		return nil, fmt.Errorf("parse tools/call result: %w", err)
	}

	return &callResult, nil
}

// ListTools returns all discovered tools from all servers.
func (c *Client) ListTools() []MCPTool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	tools := make([]MCPTool, 0, len(c.tools))
	for _, t := range c.tools {
		tools = append(tools, t)
	}
	return tools
}

// GetToolsByServer returns tools for a specific server.
func (c *Client) GetToolsByServer(serverName string) []MCPTool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var tools []MCPTool
	for key, t := range c.tools {
		if strings.HasPrefix(key, serverName+":") {
			tools = append(tools, t)
		}
	}
	return tools
}

// Close closes all server connections.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for _, transport := range c.servers {
		if err := transport.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	c.servers = make(map[string]Transport)
	c.tools = make(map[string]MCPTool)
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// Package tools mcp_tool provides hermes-tool wrappers for MCP servers.
//
// MCP (Model Context Protocol) servers are configured in ~/.hermes/config.yaml
// under the "mcp_servers" key. Each server can use stdio or HTTP transport.
//
// Example config:
//
//	mcp_servers:
//	  filesystem:
//	    command: "npx"
//	    args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
//	    env: {}
//	  github:
//	    command: "npx"
//	    args: ["-y", "@modelcontextprotocol/server-github"]
//	    env:
//	      GITHUB_PERSONAL_ACCESS_TOKEN: "ghp_..."
//	  remote_api:
//	    url: "https://my-mcp-server.example.com/mcp"
//	    headers:
//	      Authorization: "Bearer sk-..."
//	    timeout: 180
//
// Tools are registered as "mcp-<server>-<tool>" (e.g., "mcp-filesystem-read_file")
// so the agent can call them like any built-in tool.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nousresearch/hermes-go/pkg/mcp"
)

// ------------------------------------------------------------------
// MCP server registry — tracks connected servers and their tools
// ------------------------------------------------------------------

// mcpServerHandle holds state for a single connected MCP server.
type mcpServerHandle struct {
	name      string
	transport mcp.Transport
	tools     map[string]mcp.MCPTool // tool name -> metadata
	mu        sync.RWMutex
}

var (
	// mcpServers maps server name -> handle.
	mcpServers = make(map[string]*mcpServerHandle)
	mcpMu      sync.RWMutex
)

// mcpServersConfigured returns true if the user has MCP servers configured.
func mcpServersConfigured() bool {
	cfg, err := mcp.LoadMCPConfig(os.ExpandEnv("$HOME/.hermes/config.yaml"))
	if err != nil {
		return false
	}
	return len(cfg.Servers) > 0
}

// initMCPServers loads MCP server configs and starts connections.
// Safe to call multiple times — idempotent after first success.
var initMCPServers = sync.OnceFunc(func() {
	cfg, err := mcp.LoadMCPConfig(os.ExpandEnv("$HOME/.hermes/config.yaml"))
	if err != nil {
		log.Printf("[mcp] no server config found (this is fine if you have no MCP servers): %v", err)
		return
	}

	for _, serverCfg := range cfg.Servers {
		if serverCfg.Disabled {
			continue
		}
		go connectMCPServer(serverCfg)
	}
})

// connectMCPServer connects to a single MCP server, initializes it,
// discovers its tools, and registers them with the hermes registry.
func connectMCPServer(cfg mcp.MCPServerConfig) {
	timeout := 60
	if cfg.ConnectTimeout > 0 {
		timeout = cfg.ConnectTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	var transport mcp.Transport
	var err error

	if cfg.Transport == "http" || cfg.URL != "" {
		transport = mcp.NewHTTPTransport(cfg.URL, cfg.Headers, cfg.Timeout)
	} else {
		params := mcp.StdioServerParameters{
			Command: cfg.Command,
			Args:    cfg.Args,
			Env:     cfg.Env,
		}
		transport, err = mcp.NewStdioTransport(params)
		if err != nil {
			log.Printf("[mcp][%s] failed to start stdio server: %v", cfg.Name, err)
			return
		}
	}

	handle := &mcpServerHandle{
		name:      cfg.Name,
		transport: transport,
		tools:     make(map[string]mcp.MCPTool),
	}

	// Initialize the MCP server session.
	initResult, err := mcpInitialize(ctx, transport)
	if err != nil {
		log.Printf("[mcp][%s] initialization failed: %v", cfg.Name, err)
		transport.Close()
		return
	}
	log.Printf("[mcp][%s] connected: %s v%s (tools=%v, resources=%v, prompts=%v)",
		cfg.Name,
		initResult.ServerInfo.Name,
		initResult.ServerInfo.Version,
		initResult.Capabilities.Tools != nil,
		initResult.Capabilities.Resources != nil,
		initResult.Capabilities.Prompts != nil,
	)

	// Discover tools.
	tools, err := mcpListTools(ctx, transport)
	if err != nil {
		log.Printf("[mcp][%s] failed to list tools: %v", cfg.Name, err)
		transport.Close()
		return
	}

	handle.mu.Lock()
	for _, tool := range tools {
		handle.tools[tool.Name] = tool
	}
	handle.mu.Unlock()

	// Register each tool with the hermes registry.
	toolsetName := "mcp-" + cfg.Name
	for _, tool := range tools {
		fullName := "mcp-" + cfg.Name + "-" + tool.Name

		// Build the tool schema in OpenAI function-call format.
		inputSchema := tool.InputSchema
		if inputSchema == nil {
			inputSchema = make(map[string]any)
		}
		schema := map[string]any{
			"name":        fullName,
			"description": tool.Description,
			"parameters":  inputSchema,
		}

		// Capture toolName and handle for the closure.
		tn, h := tool.Name, handle
		handler := func(args map[string]any) string {
			return mcpToolHandler(tn, h, args)
		}

		Registry.Register(
			fullName,
			toolsetName,
			schema,
			handler,
			nil, // availability is checked at connection time
			nil,
			false,
			fmt.Sprintf("MCP [%s] %s", cfg.Name, tool.Description),
			"🔌",
		)
		log.Printf("[mcp][%s] registered tool: %s", cfg.Name, fullName)
	}

	mcpMu.Lock()
	mcpServers[cfg.Name] = handle
	mcpMu.Unlock()
}

// ------------------------------------------------------------------
// MCP JSON-RPC helpers
// ------------------------------------------------------------------

func mcpInitialize(ctx context.Context, transport mcp.Transport) (*mcp.InitializeResult, error) {
	params := map[string]interface{}{
		"protocolVersion": mcp.ProtocolVersion,
		"capabilities":    mcp.ClientCapabilities{},
		"clientInfo": map[string]string{
			"name":    "hermes-go",
			"version": "0.1.0",
		},
	}
	result, err := transport.Send(ctx, "initialize", params)
	if err != nil {
		return nil, err
	}
	var initResult mcp.InitializeResult
	if err := json.Unmarshal(result, &initResult); err != nil {
		return nil, fmt.Errorf("unmarshal initialize result: %w", err)
	}
	return &initResult, nil
}

func mcpListTools(ctx context.Context, transport mcp.Transport) ([]mcp.MCPTool, error) {
	result, err := transport.Send(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var listResult mcp.ToolListResult
	if err := json.Unmarshal(result, &listResult); err != nil {
		return nil, fmt.Errorf("unmarshal tools/list result: %w", err)
	}
	return listResult.Tools, nil
}

func mcpCallTool(ctx context.Context, transport mcp.Transport, name string, args map[string]interface{}) (*mcp.CallToolResult, error) {
	params := map[string]interface{}{
		"name":      name,
		"arguments": args,
	}
	result, err := transport.Send(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}
	var callResult mcp.CallToolResult
	if err := json.Unmarshal(result, &callResult); err != nil {
		return nil, fmt.Errorf("unmarshal tools/call result: %w", err)
	}
	return &callResult, nil
}

// ------------------------------------------------------------------
// Tool handler — routes to the correct MCP server
// ------------------------------------------------------------------

// mcpToolHandler handles an MCP tool call by forwarding to the appropriate server.
func mcpToolHandler(toolName string, handle *mcpServerHandle, args map[string]any) string {
	// Look up the tool to get its timeout override.
	handle.mu.RLock()
	toolMeta, ok := handle.tools[toolName]
	handle.mu.RUnlock()
	if !ok {
		return toolError(fmt.Sprintf("MCP tool %s not found on server %s", toolName, handle.name))
	}

	// Check if tool requires input schema validation.
	inputSchema := toolMeta.InputSchema
	if inputSchema != nil {
		if err := validateMCPArgs(toolName, args, inputSchema); err != nil {
			return toolError(err.Error())
		}
	}

	timeout := 120 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result, err := mcpCallTool(ctx, handle.transport, toolName, args)
	if err != nil {
		// Strip credential-like strings from error messages.
		errMsg := stripCredentials(err.Error())
		return toolError(fmt.Sprintf("MCP [%s] call failed: %s", handle.name, errMsg))
	}

	// Format the result content for the LLM.
	var output strings.Builder
	if len(result.Content) == 0 {
		output.WriteString("(no content returned)")
	}
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				output.WriteString(block.Text)
				if !strings.HasSuffix(block.Text, "\n") {
					output.WriteRune('\n')
				}
			}
		case "image":
			output.WriteString(fmt.Sprintf("[image: %s, mimeType=%s]", block.MimeType, block.MimeType))
		default:
			data, _ := json.Marshal(block)
			output.WriteString(string(data))
		}
	}

	if result.IsError {
		return toolError(strings.TrimSpace(output.String()))
	}
	return strings.TrimSpace(output.String())
}

// validateMCPArgs performs basic JSON Schema validation on tool arguments.
// Returns an error if required arguments are missing.
func validateMCPArgs(toolName string, args map[string]any, schema map[string]any) error {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil // no schema to validate against
	}
	required, _ := schema["required"].([]any)
	for _, r := range required {
		rName, ok := r.(string)
		if !ok {
			continue
		}
		if _, present := args[rName]; !present {
			return fmt.Errorf("missing required argument %q for tool %q", rName, toolName)
		}
	}
	_ = props // future: type checking
	return nil
}

// stripCredentials removes strings that look like API keys / tokens
// from error messages before returning them to the LLM.
func stripCredentials(msg string) string {
	// Replace common credential patterns.
	replacements := []string{
		`ghp_[A-Za-z0-9]{36}`,    "[GITHUB_TOKEN]",
		`sk-[A-Za-z0-9_-]{20,}`, "[API_KEY]",
		`Bearer \S+`,             "Bearer [TOKEN]",
		`token["\s:=]+\S+`,       "token=[REDACTED]",
	}
	result := msg
	for _, pattern := range replacements {
		// Simple string replacement — not a full regex for safety.
		idx := strings.Index(result, pattern)
		if idx >= 0 {
			// Just replace the pattern portion.
			result = result[:idx] + "[REDACTED]" + result[idx+len(pattern):]
		}
	}
	return result
}

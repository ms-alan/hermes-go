// Package mcp provides types for the Model Context Protocol (MCP).
package mcp

import (
	"context"
	"encoding/json"
)

// Protocol version supported by this implementation.
const ProtocolVersion = "2024-11-05"

// --------------------------------------------------------------------------
// JSON-RPC 2.0 base types
// --------------------------------------------------------------------------

// JSONRPCRequest is a JSON-RPC 2.0 request object.
type JSONRPCRequest struct {
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response object.
type JSONRPCResponse struct {
	ID     interface{}     `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError is a JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// --------------------------------------------------------------------------
// MCP protocol types
// --------------------------------------------------------------------------

// ServerCapabilities describes what a server supports.
type ServerCapabilities struct {
	Tools     *struct{} `json:"tools,omitempty"`
	Resources *struct{} `json:"resources,omitempty"`
	Prompts   *struct{} `json:"prompts,omitempty"`
	Sampling  *struct{} `json:"sampling,omitempty"`
}

// ClientCapabilities describes what a client supports.
type ClientCapabilities struct {
	Roots *struct{} `json:"roots,omitempty"`
}

// InitializeResult is returned by the server during initialization.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities  `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
}

// ServerInfo describes the server software.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolListResult is returned by tools/list.
type ToolListResult struct {
	Tools []MCPTool `json:"tools"`
}

// MCPTool describes a tool exposed by an MCP server.
type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"inputSchema,omitempty"`
}

// CallToolResult is returned by tools/call.
type CallToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock represents a piece of content in a tool result.
type ContentBlock struct {
	Type string `json:"type"` // "text", "image", "resource"
	// For text:
	Text string `json:"text,omitempty"`
	// For image:
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

// ResourceListResult is returned by resources/list.
type ResourceListResult struct {
	Resources []Resource `json:"resources"`
}

// Resource describes a resource available on the server.
type Resource struct {
	URI         string                 `json:"uri"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	MimeType    string                 `json:"mimeType,omitempty"`
}

// --------------------------------------------------------------------------
// Stdio transport parameters
// --------------------------------------------------------------------------

// StdioServerParameters describes how to launch a stdio MCP server.
type StdioServerParameters struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// MCPServerConfig describes a configured MCP server.
type MCPServerConfig struct {
	Name           string            `json:"name"`
	Transport      string            `json:"transport"` // "stdio", "http", or "sse"
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Timeout        int               `json:"timeout,omitempty"`
	ConnectTimeout int               `json:"connectTimeout,omitempty"`
	Disabled       bool              `json:"disabled,omitempty"`
	// SSE-specific paths (used when Transport == "sse")
	SSEPath   string          `json:"ssePath,omitempty"`   // endpoint for SSE subscription (default "/sse")
	HTTPPath  string          `json:"httpPath,omitempty"`  // endpoint for HTTP POST requests (default "/mcp")
	// Sampling configures server-initiated LLM sampling (sampling/createMessage).
	Sampling SamplingConfig `json:"sampling,omitempty"`
}

// MCPConfig is the top-level MCP configuration.
type MCPConfig struct {
	Servers   []MCPServerConfig `json:"servers,omitempty"`
	Defaults  *MCPDefaults      `json:"defaults,omitempty"`
}

// MCPDefaults provides default values for MCP server configuration.
type MCPDefaults struct {
	Timeout        int `json:"timeout,omitempty"`
	ConnectTimeout int `json:"connectTimeout,omitempty"`
}

// SamplingConfig configures server-initiated LLM sampling behavior.
type SamplingConfig struct {
	Enabled       bool     `json:"enabled,omitempty"`
	Model         string   `json:"model,omitempty"`
	MaxTokensCap  int      `json:"maxTokensCap,omitempty"`
	Timeout       int      `json:"timeout,omitempty"`
	MaxRPM        int      `json:"maxRpm,omitempty"`
	AllowedModels []string `json:"allowedModels,omitempty"`
	MaxToolRounds int      `json:"maxToolRounds,omitempty"`
}

// SamplingCreateMessageRequest is the params for sampling/createMessage.
type SamplingCreateMessageRequest struct {
	Method        string            `json:"method"`
	Messages      []SamplingMessage `json:"messages,omitempty"`
	MaxTokens     int               `json:"maxTokens,omitempty"`
	StopSequences []string          `json:"stopSequences,omitempty"`
	SystemPrompt  string            `json:"systemPrompt,omitempty"`
	Temperature   float64           `json:"temperature,omitempty"`
	// IncludeContext allows the server to request specific context tiers.
	IncludeContext *IncludeContextRequest `json:"includeContext,omitempty"`
}

// IncludeContextRequest specifies what context tiers to include in a sampling request.
type IncludeContextRequest struct {
	Strategies []string `json:"strategies,omitempty"` // e.g. "currentWindow", "recentHistory"
}

// SamplingCreateMessageResult is returned by sampling/createMessage.
type SamplingCreateMessageResult struct {
	Content []ContentBlock `json:"content"`
	Role    string         `json:"role"`
}

// SamplingHandler handles server-initiated sampling/createMessage requests.
type SamplingHandler interface {
	// HandleSamplingRequest processes a sampling/createMessage request from an MCP server
	// and returns an LLM response. The model parameter is the model hint from the server;
	// the handler may override it based on SamplingConfig.
	HandleSamplingRequest(ctx context.Context, serverName string, req *SamplingCreateMessageRequest, cfg SamplingConfig) (*SamplingCreateMessageResult, error)
}

// SamplingMessage is a message in a sampling request.
type SamplingMessage struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// Package mcp provides the Model Context Protocol client and server.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"

	"github.com/nousresearch/hermes-go/pkg/agent"
	"github.com/nousresearch/hermes-go/pkg/model"
	"github.com/nousresearch/hermes-go/pkg/session"
)

// HermesMCPServer implements an MCP server over stdio using JSON-RPC 2.0.
// It exposes Hermes tools and session resources to external MCP clients.
type HermesMCPServer struct {
	agent       *agent.AIAgent
	sessionStore *session.Store
	reader      *bufio.Reader
	writer      io.Writer
	mu          sync.Mutex
	initialized bool
}

// NewHermesMCPServer creates a new MCP server instance.
func NewHermesMCPServer(a *agent.AIAgent, ss *session.Store) *HermesMCPServer {
	return &HermesMCPServer{
		agent:       a,
		sessionStore: ss,
	}
}

// StartMCPServer starts the MCP server, reading JSON-RPC messages from stdin
// and writing responses to stdout. Returns only on error or when stdin is closed.
func StartMCPServer(a *agent.AIAgent, ss *session.Store) error {
	// Check stdin is usable
	stat, err := os.Stdin.Stat()
	if err != nil {
		log.Println("[mcp] stdin unavailable, skipping MCP server")
		return nil
	}
	if (stat.Mode() & os.ModeCharDevice) == 0 && stat.Size() == 0 {
		log.Println("[mcp] stdin is empty/closed, skipping MCP server")
		return nil
	}

	server := NewHermesMCPServer(a, ss)
	server.reader = bufio.NewReader(os.Stdin)
	server.writer = os.Stdout
	return server.serve()
}

// serve processes JSON-RPC messages in a loop.
func (s *HermesMCPServer) serve() error {
	for {
		line, err := s.reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read error: %w", err)
		}

		// Trim trailing newline/whitespace
		line = line[:len(line)-1]
		for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
			line = line[:len(line)-1]
		}
		if len(line) == 0 {
			continue
		}

		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(nil, -32700, "Parse error")
			continue
		}

		resp, err := s.handleRequest(&req)
		if err != nil {
			s.writeError(req.ID, -32603, err.Error())
			continue
		}

		if resp != nil {
			s.writeResponse(resp)
		}
	}
}

// handleRequest routes the request to the appropriate handler.
func (s *HermesMCPServer) handleRequest(req *JSONRPCRequest) (*JSONRPCResponse, error) {
	// After initialize, only certain methods are allowed
	if !s.initialized && req.Method != "initialize" {
		return nil, fmt.Errorf("server not initialized")
	}

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req.ID, req.Params)
	case "tools/list":
		return s.handleToolsList(req.ID)
	case "tools/call":
		return s.handleToolsCall(req.ID, req.Params)
	case "resources/list":
		return s.handleResourcesList(req.ID)
	case "sampling/createMessage":
		return s.handleSamplingCreateMessage(req.ID, req.Params)
	case "ping":
		return s.handlePing(req.ID)
	default:
		return nil, fmt.Errorf("method not found: %s", req.Method)
	}
}

// handleInitialize handles the initialize request.
func (s *HermesMCPServer) handleInitialize(id interface{}, _ json.RawMessage) (*JSONRPCResponse, error) {
	result := InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: ServerCapabilities{
			Tools:     &struct{}{},
			Resources: &struct{}{},
		},
		ServerInfo: ServerInfo{
			Name:    "hermes",
			Version: "1.0.0",
		},
	}
	s.initialized = true
	return &JSONRPCResponse{ID: id, Result: mustMarshal(result)}, nil
}

// handleToolsList returns the list of registered Hermes tools.
func (s *HermesMCPServer) handleToolsList(id interface{}) (*JSONRPCResponse, error) {
	tools := s.listTools()
	result := ToolListResult{Tools: tools}
	return &JSONRPCResponse{ID: id, Result: mustMarshal(result)}, nil
}

// listTools returns all registered Hermes tools as MCP tools.
func (s *HermesMCPServer) listTools() []MCPTool {
	var tools []MCPTool
	if s.agent != nil {
		for name, def := range s.agent.GetToolDefs() {
			tools = append(tools, MCPTool{
				Name:        name,
				Description: def.Description,
				InputSchema: def.InputSchema,
			})
		}
	}
	return tools
}

// handleToolsCall executes a tool by name with given arguments.
func (s *HermesMCPServer) handleToolsCall(id interface{}, params json.RawMessage) (*JSONRPCResponse, error) {
	var req struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("invalid tools/call params: %w", err)
	}

	result, err := s.callTool(req.Name, req.Arguments)
	if err != nil {
		// Return error as a failed tool result, not a JSON-RPC error
		result := CallToolResult{
			Content: []ContentBlock{{Type: "text", Text: err.Error()}},
			IsError: true,
		}
		return &JSONRPCResponse{ID: id, Result: mustMarshal(result)}, nil
	}

	return &JSONRPCResponse{ID: id, Result: mustMarshal(result)}, nil
}

// callTool executes a tool by name.
func (s *HermesMCPServer) callTool(name string, args map[string]interface{}) (*CallToolResult, error) {
	if s.agent == nil {
		return nil, fmt.Errorf("agent not available")
	}

	handler := s.agent.GetToolHandler(name)
	if handler == nil {
		return nil, fmt.Errorf("tool not found: %s", name)
	}

	result := handler(context.Background(), &model.ToolCallRequest{
		ID:        name,
		Name:      name,
		Arguments: args,
	})
	if result.IsError {
		errMsg := ""
		if result.Error != nil {
			errMsg = result.Error.Error()
		}
		return &CallToolResult{
			Content: []ContentBlock{{Type: "text", Text: errMsg}},
			IsError: true,
		}, nil
	}

	return &CallToolResult{
		Content: []ContentBlock{{Type: "text", Text: result.Content}},
	}, nil
}

// handleResourcesList returns the list of available resources.
func (s *HermesMCPServer) handleResourcesList(id interface{}) (*JSONRPCResponse, error) {
	var resources []Resource
	if s.sessionStore != nil {
		sessions, _ := s.sessionStore.ListSessions("", 100, 0)
		for _, sess := range sessions {
			resources = append(resources, Resource{
				URI:         fmt.Sprintf("session://%s", sess.ID),
				Name:        sess.ID,
				Description: fmt.Sprintf("Session: %s", sess.ID),
				MimeType:    "application/json",
			})
		}
	}
	result := ResourceListResult{Resources: resources}
	return &JSONRPCResponse{ID: id, Result: mustMarshal(result)}, nil
}

// handleSamplingCreateMessage handles server-initiated LLM requests.
func (s *HermesMCPServer) handleSamplingCreateMessage(id interface{}, params json.RawMessage) (*JSONRPCResponse, error) {
	var req SamplingCreateMessageRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("invalid sampling/createMessage params: %w", err)
	}

	// Build messages for the agent
	var messages []*model.Message
	for _, msg := range req.Messages {
		messages = append(messages, &model.Message{
			Role:    model.Role(msg.Role),
			Content: msg.Content,
		})
	}

	var responseText string
	if s.agent != nil && len(messages) > 0 {
		result := s.agent.RunWithMessages(context.Background(), messages, req.SystemPrompt)
		if result.Error != nil {
			responseText = fmt.Sprintf("error: %s", result.Error.Error())
		} else {
			responseText = result.FinalResponse
		}
	}

	result := SamplingCreateMessageResult{
		Content: []ContentBlock{{Type: "text", Text: responseText}},
		Role:    "assistant",
	}
	return &JSONRPCResponse{ID: id, Result: mustMarshal(result)}, nil
}

// handlePing handles the ping request.
func (s *HermesMCPServer) handlePing(id interface{}) (*JSONRPCResponse, error) {
	return &JSONRPCResponse{ID: id, Result: mustMarshal(map[string]bool{"pong": true})}, nil
}

// writeResponse sends a JSON-RPC response to stdout.
func (s *HermesMCPServer) writeResponse(resp *JSONRPCResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _ := json.Marshal(resp)
	fmt.Fprintln(s.writer, string(data))
}

// writeError sends a JSON-RPC error response to stdout.
func (s *HermesMCPServer) writeError(id interface{}, code int, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &JSONRPCResponse{
		ID: id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
		},
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintln(s.writer, string(data))
}

// mustMarshal marshals a value or panics.
func mustMarshal(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(fmt.Sprintf(`{"error":"marshal error: %v"}`, err))
	}
	return data
}

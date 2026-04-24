// Package tui_rpc implements a JSON-RPC 2.0 server over stdio for the TUI gateway.
// It bridges the React/Ink TUI frontend to hermes-go's delegation control APIs.
package tui_rpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/nousresearch/hermes-go/pkg/agent"
)

// RPCServer is a JSON-RPC 2.0 server over stdio.
type RPCServer struct {
	scanner     *bufio.Scanner
	writer      io.Writer
	logger      *slog.Logger
	wg          sync.WaitGroup
	shutdownCh  chan struct{}
	mu          sync.Mutex
}

// JSON-RPC 2.0 message types
type rpcRequest struct {
	ID     any     `json:"id"`
	Method string  `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     any      `json:"id"`
	Result any      `json:"result,omitempty"`
	Error  *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcNotification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInternalError  = -32603
)

// NewRPCServer creates a new JSON-RPC 2.0 server using stdin/stdout.
func NewRPCServer(logger *slog.Logger) *RPCServer {
	// Use a larger buffer for long requests
	scanner := bufio.NewScanner(os.Stdin)
	const maxCapacity = 1024 * 1024
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	return &RPCServer{
		scanner:    scanner,
		writer:     os.Stdout,
		logger:     logger,
		shutdownCh: make(chan struct{}),
	}
}

// Serve starts the JSON-RPC server loop. It reads requests from stdin
// and writes responses to stdout. Stderr is used for logging.
func (s *RPCServer) Serve() error {
	for {
		select {
		case <-s.shutdownCh:
			return nil
		default:
		}

		if !s.scanner.Scan() {
			if err := s.scanner.Err(); err != nil {
				if err == io.EOF {
					return nil
				}
				return fmt.Errorf("scanner error: %w", err)
			}
			return nil
		}

		line := s.scanner.Text()
		if line == "" {
			continue
		}

		if err := s.handleLine(line); err != nil {
			s.logger.Error("failed to handle line", "error", err)
		}
	}
}

// Shutdown stops the server gracefully.
func (s *RPCServer) Shutdown() {
	close(s.shutdownCh)
}

func (s *RPCServer) handleLine(line string) error {
	if line == "" {
		return nil
	}

	var req rpcRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		return s.sendError(nil, CodeParseError, "Parse error: "+err.Error())
	}

	// Notifications have no ID — fire and forget.
	if req.ID == nil {
		s.handleRequest(&req)
		return nil
	}

	return s.handleRequest(&req)
}

func (s *RPCServer) handleRequest(req *rpcRequest) error {
	var result any
	var errMsg string
	var errCode int

	switch req.Method {
	case "delegation.status":
		result, errCode, errMsg = s.delegationStatus()

	case "delegation.pause":
		result, errCode, errMsg = s.delegationPause(req.Params)

	case "subagent.interrupt":
		result, errCode, errMsg = s.subagentInterrupt(req.Params)

	default:
		errCode = CodeMethodNotFound
		errMsg = fmt.Sprintf("method %q not found", req.Method)
	}

	if errMsg != "" {
		return s.sendError(req.ID, errCode, errMsg)
	}

	return s.sendResult(req.ID, result)
}

func (s *RPCServer) delegationStatus() (any, int, string) {
	subagents := agent.ListActiveSubagents()

	records := make([]map[string]any, len(subagents))
	for i, r := range subagents {
		records[i] = map[string]any{
			"subagent_id": r.SubagentID,
			"parent_id":   r.ParentID,
			"depth":       r.Depth,
			"goal":        r.Goal,
			"model":       r.Model,
			"toolsets":    r.Toolsets,
			"started_at":   r.StartedAt.Format(time.RFC3339),
			"status":      r.Status,
			"tool_count":  r.ToolCount,
			"last_tool":   r.LastTool,
		}
	}

	return map[string]any{
		"active":              records,
		"paused":              agent.IsSpawnPaused(),
		"max_spawn_depth":     1, // constant in delegate.go
		"max_concurrent_children": 3, // defaultMaxConcurrent constant
	}, 0, ""
}

func (s *RPCServer) delegationPause(params json.RawMessage) (any, int, string) {
	var p struct {
		Paused *bool `json:"paused"`
	}
	if params != nil {
		json.Unmarshal(params, &p)
	}

	// Default: pause (paused=true)
	paused := true
	if p.Paused != nil {
		paused = *p.Paused
	}

	agent.SetSpawnPaused(paused)
	return map[string]any{"paused": agent.IsSpawnPaused()}, 0, ""
}

func (s *RPCServer) subagentInterrupt(params json.RawMessage) (any, int, string) {
	var p struct {
		SubagentID string `json:"subagent_id"`
	}
	if params == nil {
		return nil, CodeInvalidRequest, "params required"
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, CodeInvalidRequest, "invalid params: "+err.Error()
	}
	if p.SubagentID == "" {
		return nil, CodeInvalidRequest, "subagent_id is required"
	}

	ok := agent.InterruptSubagent(p.SubagentID)
	return map[string]any{"interrupted": ok, "subagent_id": p.SubagentID}, 0, ""
}

func (s *RPCServer) sendResult(id any, result any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	resp := rpcResponse{
		ID:     id,
		Result: result,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal error: %w", err)
	}
	_, err = fmt.Fprintln(s.writer, string(data))
	return err
}

func (s *RPCServer) sendError(id any, code int, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	resp := rpcResponse{
		ID: id,
		Error: &rpcError{
			Code:    code,
			Message: message,
		},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal error: %w", err)
	}
	_, err = fmt.Fprintln(s.writer, string(data))
	return err
}

// Package tools provides a central registry for all hermes-go tools.
//
// The bridge subpackage exposes a single function that registers all
// built-in tools into an external agent.AIAgent instance.
// This avoids duplicating the registration logic across cmd packages.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nousresearch/hermes-go/pkg/agent"
	"github.com/nousresearch/hermes-go/pkg/model"
)

// RegisterBuiltinToolsToAgent registers all tools from the tools registry
// into the given AIAgent, bridging from the tools package handlers to the
// agent's ToolHandler signature.
func RegisterBuiltinToolsToAgent(aiAgent *agent.AIAgent) {
	registry := Registry

	for _, name := range registry.List() {
		entry := registry.GetEntry(name)
		if entry == nil {
			continue
		}

		// Skip tools whose CheckFn says they are unavailable (e.g. missing env vars).
		if entry.CheckFn != nil && !entry.CheckFn() {
			continue
		}

		handler := entry.Handler
		wrapped := func(ctx context.Context, req *model.ToolCallRequest) *model.ToolResult {
			output := handler(req.Arguments)
			// Detect error responses: handlers return {"error": "msg"} on failure.
			if isToolError(output) {
				return model.NewToolError(fmt.Errorf("%s", stripError(output)))
			}
			return model.NewToolResult(output)
		}

		def := &agent.ToolDef{
			Name:        entry.Schema.Name,
			Description: entry.Description,
			InputSchema: entry.Schema.Parameters,
		}

		aiAgent.RegisterTool(name, wrapped, def)
	}
}

// isToolError returns true if s looks like {"error": ...}.
func isToolError(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return false
	}
	_, ok := m["error"]
	return ok
}

// stripError extracts the "error" field value from a JSON error string.
func stripError(s string) string {
	s = strings.TrimSpace(s)
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return s
	}
	if v, ok := m["error"].(string); ok {
		return v
	}
	return s
}

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

// DEFAULT_TOOLSETS is the set of toolsets loaded by default when no explicit
// list is provided to RegisterBuiltinToolsToAgent. Mirrors hermes-agent's
// minimal default: terminal + file operations + web search.
var DEFAULT_TOOLSETS = []string{
	"terminal",
	"file",
	"web",
}

// GetToolsForToolsets returns tool names that belong to any of the given
// toolsets. Special handling: "mcp-*" matches all MCP dynamic tools.
func GetToolsForToolsets(registry *ToolRegistry, toolsets []string) []string {
	if len(toolsets) == 0 {
		return registry.List()
	}

	toolsetSet := make(map[string]bool)
	includeMCP := false
	for _, ts := range toolsets {
		if ts == "mcp" || strings.HasPrefix(ts, "mcp-") {
			includeMCP = true
			continue
		}
		toolsetSet[ts] = true
	}

	var names []string
	for _, name := range registry.List() {
		entry := registry.GetEntry(name)
		if entry == nil {
			continue
		}
		// Always include MCP dynamic tools if includeMCP is set.
		if strings.HasPrefix(entry.Toolset, "mcp-") {
			if includeMCP {
				names = append(names, name)
			}
			continue
		}
		// Check if this tool's toolset is in the allowlist.
		if toolsetSet[entry.Toolset] {
			names = append(names, name)
		}
	}
	return names
}

// RegisterBuiltinToolsToAgent registers tools from the tools registry into
// the given AIAgent, bridging from the tools package handlers to the agent's
// ToolHandler signature.
//
// The toolsets parameter controls which toolsets are loaded:
//   - nil or empty: loads all registered tools (full toolkit).
//   - non-nil: only loads tools whose toolset is in the list.
//     Special value "mcp" or "mcp-*" includes all MCP dynamic tools.
//
// When toolsets is non-nil, only tools from the specified toolsets are
// registered; all others are silently skipped. This matches hermes-agent's
// progressive loading behaviour where the agent is configured with a toolset
// list at startup rather than loading everything unconditionally.
func RegisterBuiltinToolsToAgent(aiAgent *agent.AIAgent, toolsets []string) {
	registry := Registry
	names := GetToolsForToolsets(registry, toolsets)

	for _, name := range names {
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
			// Propagate delivery origin to the async-safe store so tools
			// that don't receive context can still access it (e.g. cron tool).
			origin := DeliveryOriginFromContext(ctx)
			SetCurrentDeliveryOrigin(origin)
			defer SetCurrentDeliveryOrigin(nil)

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

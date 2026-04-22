// Package tools provides a central registry for all hermes-go tools.
//
// The bridge subpackage exposes a single function that registers all
// built-in tools into an external agent.AIAgent instance.
// This avoids duplicating the registration logic across cmd packages.
package tools

import (
	"context"

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

		// Bridge tools.ToolHandler (sync string-returning) → agent.ToolHandler
		handler := entry.Handler
		wrapped := func(ctx context.Context, req *model.ToolCallRequest) *model.ToolResult {
			output := handler(req.Arguments)
			return &model.ToolResult{Content: output}
		}

		def := &agent.ToolDef{
			Name:        entry.Schema.Name,
			Description: entry.Description,
			InputSchema: entry.Schema.Parameters,
		}

		aiAgent.RegisterTool(name, wrapped, def)
	}
}

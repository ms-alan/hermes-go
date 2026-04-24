// tools.go is a thin shim that delegates to the tools package.
// kept here so cmd/hermes stays decoupled from the internal registry layout.
package main

import (
	"github.com/nousresearch/hermes-go/pkg/agent"
	"github.com/nousresearch/hermes-go/pkg/tools"
)

// registerBuiltinTools is called by newREPL to wire built-in tools into the agent.
// Pass nil for toolsets to load all available tools; or a list like
// []string{"terminal", "file", "web"} to restrict to specific toolsets.
func registerBuiltinTools(aiAgent *agent.AIAgent, toolsets []string) {
	tools.RegisterBuiltinToolsToAgent(aiAgent, toolsets)
}

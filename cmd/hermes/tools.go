// tools.go is a thin shim that delegates to the tools package.
// kept here so cmd/hermes stays decoupled from the internal registry layout.
package main

import (
	"github.com/nousresearch/hermes-go/pkg/agent"
	"github.com/nousresearch/hermes-go/pkg/tools"
)

// registerBuiltinTools is called by newREPL to wire built-in tools into the agent.
func registerBuiltinTools(aiAgent *agent.AIAgent) {
	tools.RegisterBuiltinToolsToAgent(aiAgent)
}

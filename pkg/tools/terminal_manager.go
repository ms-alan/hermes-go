package tools

import (
	"context"
	"time"

	"github.com/nousresearch/hermes-go/pkg/terminal"
)

// terminalManager is the global multi-backend terminal manager.
var terminalManager = terminal.NewManager()

// TerminalManager returns the global terminal manager.
// Tools and external packages can use this to register additional backends.
func TerminalManager() *terminal.Manager {
	return terminalManager
}

// runTerminalCommand runs a command using the specified backend.
func runTerminalCommand(args map[string]any) string {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return toolError("terminal requires a 'command' argument")
	}

	backend := "local"
	if b, ok := args["backend"].(string); ok {
		backend = b
	}

	timeout := 60
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	cwd := ""
	if w, ok := args["cwd"].(string); ok {
		cwd = w
	}

	ctx := context.Background()
	pty := false
	if p, ok := args["pty"].(bool); ok {
		pty = p
	}
	stdout, stderr, exitCode, err := terminalManager.Run(ctx, backend, command, time.Duration(timeout)*time.Second, pty)
	if err != nil {
		return toolResultData(map[string]any{
			"command":   command,
			"backend":   backend,
			"stdout":    stdout,
			"stderr":    stderr,
			"exitCode": -1,
			"error":    err.Error(),
			"success":  false,
		})
	}

	return toolResultData(map[string]any{
		"command":   command,
		"backend":   backend,
		"cwd":       cwd,
		"stdout":    stdout,
		"stderr":    stderr,
		"exitCode": exitCode,
		"success":   exitCode == 0,
	})
}

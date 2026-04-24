package tools

import (
	"fmt"

	"github.com/nousresearch/hermes-go/pkg/interrupt"
)

// interruptToolSchema defines the interrupt tool.
var interruptSchema = map[string]any{
	"name":        "interrupt",
	"description": "Signal or clear an interrupt for the current goroutine. Long-running tools check IsInterrupted() and return early when set. Used by the agent to cancel stuck operations without killing other concurrent sessions.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action: 'set' to signal interrupt, 'clear' to cancel, 'check' to query current state",
				"enum":        []any{"set", "clear", "check"},
			},
		},
		"required": []any{"action"},
	},
}

// interruptToolHandler handles interrupt actions.
func interruptHandler(args map[string]any) string {
	action, _ := args["action"].(string)

	switch action {
	case "set":
		interrupt.SetInterrupt(true)
		return toolResult("set", map[string]any{"status": "interrupt_signaled"})
	case "clear":
		interrupt.ClearInterrupt()
		return toolResult("cleared", map[string]any{"status": "interrupt_cleared"})
	case "check":
		isSet := interrupt.IsInterrupted()
		return toolResultData(map[string]any{
			"interrupted": isSet,
			"status":      map[bool]string{true: "interrupted", false: "running"}[isSet],
		})
	default:
		return toolError(fmt.Sprintf("unknown action %q — use: set, clear, check", action))
	}
}

package tools

import (
	"encoding/json"
	"fmt"

	"github.com/nousresearch/hermes-go/pkg/memory"
)

var memorySchema = map[string]any{
	"name":        "memory",
	"description": "Persistent curated memory for the agent — two stores: MEMORY.md (agent's personal notes) and USER.md (user profile). Use 'add' to save new facts/preferences as entries. Use 'replace' or 'remove' to update entries using unique substring matching. Mid-session writes persist to disk immediately but do NOT change the system prompt until the next session. Character limits enforced: MEMORY 2,200 chars, USER 1,375 chars. Entries separated by §.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action: 'add' (append entry), 'replace' (replace matching entry), 'remove' (delete matching entry), 'read' (show entries)",
				"enum":        []any{"add", "replace", "remove", "read"},
			},
			"target": map[string]any{
				"type":        "string",
				"description": "Memory store: 'memory' (agent notes) or 'user' (user profile)",
				"enum":        []any{"memory", "user"},
				"default":     "memory",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content for add/replace actions. Required for add and replace. Should be a complete, self-contained entry.",
			},
			"old_text": map[string]any{
				"type":        "string",
				"description": "Unique substring to match for replace/remove. Must match exactly one entry.",
			},
		},
		"required": []any{"action"},
	},
}

// memoryHandler manages persistent curated memory (MEMORY.md and USER.md).
// Signature: func(args map[string]any) string (matches Registry.Handler type).
func memoryHandler(args map[string]any) string {
	ms := memory.GetMemoryStore()
	if ms == nil {
		return fmt.Sprintf(`{"error": "memory store not initialized"}`)
	}

	action, _ := args["action"].(string)
	target, _ := args["target"].(string)
	if target == "" {
		target = "memory"
	}
	content, _ := args["content"].(string)
	oldText, _ := args["old_text"].(string)

	var result memory.Action
	switch action {
	case "add":
		result = ms.Add(target, content)
	case "replace":
		result = ms.Replace(target, oldText, content)
	case "remove":
		result = ms.Remove(target, oldText)
	case "read":
		result = ms.Read(target)
	default:
		return fmt.Sprintf(`{"error": "unknown action '%s': must be add, replace, remove, or read"}`, action)
	}

	resp, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf(`{"error": "failed to marshal result: %v"}`, err)
	}
	return string(resp)
}

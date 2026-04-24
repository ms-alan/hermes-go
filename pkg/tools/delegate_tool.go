package tools

var delegateTaskSchema = map[string]any{
	"name":        "delegate_task",
	"description": "Spawn a sub-agent to work on a task in isolation. The sub-agent gets a fresh session, restricted tools, and its own model. Supports single goal or parallel batch (up to 3 tasks concurrently). Results are summarized and returned to the parent.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"goal": map[string]any{
				"type":        "string",
				"description": "The task description for a single sub-agent",
			},
			"context": map[string]any{
				"type":        "string",
				"description": "Background context passed to the sub-agent",
			},
			"tasks": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"goal":      map[string]any{"type": "string"},
						"context":   map[string]any{"type": "string"},
						"toolsets":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"role":      map[string]any{"type": "string"},
						"model":     map[string]any{"type": "string"},
					},
				},
				"description": "Batch of tasks to run in parallel (max 3)",
			},
			"toolsets": map[string]any{
				"type":        "string",
				"description": "Comma-separated toolsets to enable for the sub-agent (default: inherit from parent)",
			},
			"role": map[string]any{
				"type":        "string",
				"description": "'leaf' (default, cannot delegate further) or 'orchestrator' (can delegate more workers, subject to depth limit)",
				"default":     "leaf",
			},
			"model": map[string]any{
				"type":        "string",
				"description": "Model override for the sub-agent (e.g. 'gpt-4o', 'claude-sonnet-4')",
			},
			"max_iterations": map[string]any{
				"type":        "integer",
				"description": "Max tool-call iterations per sub-agent (default: 50)",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Max seconds before sub-agent is cancelled (default: 300)",
			},
		},
	},
}

func delegateTaskHandler(args map[string]any) string {
	result := map[string]any{
		"status":  "unavailable",
		"message": "delegate_task is not yet implemented in hermes-go. " +
			"To request this feature, please describe your use case. " +
			"Expected behavior: spawns a sub-agent in an isolated session with " +
			"restricted toolsets, runs up to 3 parallel tasks, aggregates results. " +
			"Configuration: set delegation.provider/model in config.yaml.",
		"alternatives": []map[string]string{
			{
				"tool":    "session_search",
				"use":     "search across past sessions for context",
			},
			{
				"tool":    "mixture_of_agents",
				"use":     "parallel multi-model synthesis for complex tasks",
			},
			{
				"tool":    "send_message",
				"use":     "deliver results to QQ/Telegram/Discord",
			},
		},
	}
	return toolResultData(result)
}

package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/nousresearch/hermes-go/pkg/agent"
)

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
	// Determine if this is a batch or single task.
	var requests []*agent.DelegateRequest

	if tasksRaw, ok := args["tasks"].([]any); ok && len(tasksRaw) > 0 {
		// Batch mode: up to 3 parallel tasks.
		for i, t := range tasksRaw {
			if i >= 3 {
				break // max 3 concurrent
			}
			task, ok := t.(map[string]any)
			if !ok {
				continue
			}
			req := buildDelegateRequest(task)
			requests = append(requests, req)
		}
	} else {
		// Single goal mode.
		req := buildDelegateRequest(args)
		requests = append(requests, req)
	}

	if len(requests) == 0 {
		return toolError("no valid tasks provided")
	}

	results, err := agent.Delegate(context.Background(), requests...)
	if err != nil {
		return toolError("delegate failed: " + err.Error())
	}

	// Format results.
	var lines []string
	for _, r := range results {
		switch r.Status {
		case "success":
			summary := r.Result
			if len(summary) > 500 {
				summary = summary[:500] + "..."
			}
			lines = append(lines, fmt.Sprintf("✅ [%s] (%ds)\n%s", r.Goal, r.Duration, summary))
		case "timeout":
			lines = append(lines, fmt.Sprintf("⏱️ [%s] TIMEOUT after %ds", r.Goal, r.Duration))
		case "error":
			errMsg := r.Error
			if len(errMsg) > 200 {
				errMsg = errMsg[:200] + "..."
			}
			lines = append(lines, fmt.Sprintf("❌ [%s] ERROR: %s", r.Goal, errMsg))
		}
	}

	return toolResultData(map[string]any{
		"results": results,
		"summary": strings.Join(lines, "\n\n"),
	})
}

func buildDelegateRequest(args map[string]any) *agent.DelegateRequest {
	goal, _ := args["goal"].(string)
	req := &agent.DelegateRequest{Goal: goal}
	if v, ok := args["context"].(string); ok {
		req.Context = v
	}
	if v, ok := args["toolsets"].(string); ok {
		req.Toolsets = v
	}
	if v, ok := args["role"].(string); ok {
		req.Role = v
	}
	if v, ok := args["model"].(string); ok {
		req.Model = v
	}
	if v, ok := args["max_iterations"].(float64); ok {
		req.MaxIterations = int(v)
	}
	if v, ok := args["timeout"].(float64); ok {
		req.TimeoutSecs = int(v)
	}
	return req
}

func init() {
	Register(
		"delegate_task",
		"builtin",
		delegateTaskSchema,
		delegateTaskHandler,
		nil,
		nil,
		false,
		"Spawn autonomous sub-agents for parallel task execution",
		"🤖",
	)
}

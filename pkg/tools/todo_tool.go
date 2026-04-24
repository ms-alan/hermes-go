package tools

import "encoding/json"

// globalTodoStore is the shared store instance. It is per-session in hermes-agent;
// here we use a global singleton (one per gateway process).
var globalTodoStore = &TodoStore{}

var todoSchema = map[string]any{
	"name":        "todo",
	"description": "Manage a persistent task list. Call with no todos field to read the current list. Call with todos to write (merge=false replaces entirely, merge=true updates by id). Returns the full list with summary counts after every write.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"todos": map[string]any{
				"type": "array",
				"description": "List of todo items to write. Each item needs {id, content, status}. Valid statuses: pending / in_progress / completed / cancelled. Omit this field entirely to read the current list.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":      map[string]any{"type": "string", "description": "Unique identifier for the task"},
						"content": map[string]any{"type": "string", "description": "Task description"},
						"status": map[string]any{
							"type":        "string",
							"description": "One of: pending, in_progress, completed, cancelled",
						},
					},
					"required": []any{"id"},
				},
			},
			"merge": map[string]any{
				"type":        "boolean",
				"description": "If false (default), replace entire list. If true, update by id and append new items.",
			},
		},
	},
}

func todoHandler(args map[string]any) string {
	type todoInput struct {
		ID      string `json:"id"`
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	var inputTodos []todoInput
	if v, ok := args["todos"]; ok && v != nil {
		data, _ := json.Marshal(v)
		json.Unmarshal(data, &inputTodos)
	}

	merge, _ := args["merge"].(bool)

	if len(inputTodos) == 0 {
		// Read mode
		return formatTodoResult(globalTodoStore.Read())
	}

	in := make([]TodoItem, len(inputTodos))
	for i, t := range inputTodos {
		in[i] = TodoItem{ID: t.ID, Content: t.Content, Status: t.Status}
	}
	return formatTodoResult(globalTodoStore.Write(in, merge))
}

func formatTodoResult(items []TodoItem) string {
	var pending, inProgress, completed, cancelled int
	for _, item := range items {
		switch item.Status {
		case TodoStatusPending:
			pending++
		case TodoStatusInProgress:
			inProgress++
		case TodoStatusCompleted:
			completed++
		case TodoStatusCancelled:
			cancelled++
		}
	}
	return toolResultData(map[string]any{
		"todos": items,
		"summary": map[string]int{
			"total":       len(items),
			"pending":     pending,
			"in_progress": inProgress,
			"completed":   completed,
			"cancelled":   cancelled,
		},
	})
}

package tools

import (
	"fmt"
)

// clarifyTool asks the user a clarifying question when the agent needs more information.
// Works in both CLI (interactive) and gateway (queued for delivery) contexts.
var clarifySchema = map[string]any{
	"name":        "clarify",
	"description": "Ask the user a question when you need clarification, feedback, or a decision before proceeding. Supports multiple choice (up to 4 options) or free-form text. In CLI sessions, prints the question and blocks. In gateway sessions, queues the question for the platform to deliver.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{
				"type":        "string",
				"description": "The question to present to the user.",
			},
			"choices": map[string]any{
				"type": "array",
				"items": map[string]any{"type": "string"},
				"description": "Up to 4 answer choices. Omit to ask a free-form question.",
			},
			"deliver": map[string]any{
				"type":        "string",
				"description": "Delivery target. 'origin' (current chat, default), 'local' (save only), or 'platform:chat_id:thread_id' for explicit targeting.",
			},
		},
		"required": []any{"question"},
	},
}

// clarifyHandler presents a question to the user.
// In CLI REPL context, this blocks until the user types a response.
// In gateway/cron context, this queues the question to the user's platform.
func clarifyHandler(args map[string]any) string {
	question, _ := args["question"].(string)
	if question == "" {
		return toolError("question is required")
	}

	choicesRaw, ok := args["choices"].([]any)
	var choices []string
	if ok {
		for _, c := range choicesRaw {
			if s, ok := c.(string); ok {
				choices = append(choices, s)
			}
		}
	}

	if len(choices) > 0 && len(choices) <= 4 {
		return clarifyMultipleChoice(question, choices)
	}
	return clarifyFreeform(question)
}

func clarifyMultipleChoice(question string, choices []string) string {
	// Build a formatted question with numbered choices
	lines := []string{question, ""}
	for i, c := range choices {
		lines = append(lines, fmt.Sprintf("  %d. %s", i+1, c))
	}
	lines = append(lines, "", "(type the number or your answer)")
	full := fmt.Sprintf("\n%s\n", joinLines(lines))
	return toolResultData(map[string]any{
		"type":     "multiple_choice",
		"question":  question,
		"choices":   choices,
		"prompt":   full,
		"waiting":  true,
	})
}

func clarifyFreeform(question string) string {
	return toolResultData(map[string]any{
		"type":     "freeform",
		"question": question,
		"waiting":  true,
	})
}

// joinLines is a helper to join strings with newlines.
func joinLines(parts []string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "\n"
		}
		result += p
	}
	return result
}

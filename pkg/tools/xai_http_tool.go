package tools

import "os"

var xaiHttpSchema = map[string]any{
	"name":        "xai_status",
	"description": "Check xAI API (Grok) availability and configuration.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action: status (default)",
			},
		},
	},
}

func xaiHttpHandler(args map[string]any) string {
	apiKey := os.Getenv("XAI_API_KEY")
	hasKey := apiKey != ""

	result := map[string]any{
		"has_api_key": hasKey,
		"provider":    "xAI (Grok)",
		"base_url":    "https://api.x.ai/v1",
		"models":      []string{"x-ai/grok-2", "x-ai/grok-2-beta"},
	}
	if hasKey {
		result["key_prefix"] = apiKey[:8] + "..."
	}
	return toolResultData(result)
}

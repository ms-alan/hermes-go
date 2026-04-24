package tools

import "os"

var openrouterClientSchema = map[string]any{
	"name":        "openrouter_status",
	"description": "Check OpenRouter API key status and available models. Returns key presence and the default models available via OpenRouter.",
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

func openrouterClientHandler(args map[string]any) string {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	hasKey := apiKey != ""

	result := map[string]any{
		"has_api_key": hasKey,
		"provider":    "openrouter",
		"base_url":   "https://openrouter.ai/api/v1",
		"default_models": []string{
			"openai/gpt-4o",
			"anthropic/claude-sonnet-4",
			"google/gemini-2.0-flash",
			"deepseek/deepseek-chat-v3",
		},
	}
	if hasKey {
		result["key_prefix"] = apiKey[:8] + "..."
	}
	return toolResultData(result)
}

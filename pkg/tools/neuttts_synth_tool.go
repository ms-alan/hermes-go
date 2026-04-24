package tools

import "os"

var neutttsSynthSchema = map[string]any{
	"name":        "neutts_synth",
	"description": "Neuttts is a neural TTS engine. This tool checks configuration and available voices. Audio generation is handled by the existing tts_tool.",
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

func neutttsSynthHandler(args map[string]any) string {
	apiKey := os.Getenv("NEUTTTS_API_KEY")
	hasKey := apiKey != ""

	result := map[string]any{
		"has_api_key": hasKey,
		"provider":    "Neuttts",
		"base_url":    "https://api.neuttts.com/v1",
		"note":        "Audio generation is handled by the tts tool. Neuttts provider can be configured in tts config.",
	}
	if hasKey {
		result["key_prefix"] = apiKey[:8] + "..."
	}
	return toolResultData(result)
}

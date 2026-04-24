package tools

import "os"

var transcriptionSchema = map[string]any{
	"name":        "transcription",
	"description": "Transcribe audio to text. Supports multiple backends: local (faster-whisper), OpenAI (whisper-1), Groq (whisper-large-v3), Gemini, Azure, Deepgram.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"audio_path": map[string]any{
				"type":        "string",
				"description": "Path to the audio file (mp3, wav, ogg, m4a, flac)",
			},
			"provider": map[string]any{
				"type":        "string",
				"description": "Provider: auto (default), local, openai, groq, gemini, azure, deepgram",
			},
		},
		"required": []any{"audio_path"},
	},
}

func transcriptionHandler(args map[string]any) string {
	audioPath, _ := args["audio_path"].(string)
	provider, _ := args["provider"].(string)

	// Check what's available
	openaiKey := os.Getenv("OPENAI_API_KEY")
	groqKey := os.Getenv("GROQ_API_KEY")
	geminiKey := os.Getenv("GEMINI_API_KEY")

	result := map[string]any{
		"status":    "stub",
		"audio_path": audioPath,
		"provider":  provider,
		"available_backends": map[string]bool{
			"local":   false, // requires faster-whisper
			"openai":  openaiKey != "",
			"groq":    groqKey != "",
			"gemini":  geminiKey != "",
			"azure":   false,
			"deepgram": false,
		},
		"message": "Transcription is a stub — install faster-whisper or set API keys to enable. Supported: auto/local/openai/groq.",
	}
	return toolResultData(result)
}

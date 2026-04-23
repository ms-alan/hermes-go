package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nousresearch/hermes-go/config"
)

// --------------------------------------------------------------------------
// text_to_speech — convert text to audio
// --------------------------------------------------------------------------

var textToSpeechSchema = map[string]any{
	"name":        "text_to_speech",
	"description": "Convert text to speech audio using MiniMax TTS API. The audio is saved to a file (MP3/WAV) and can be sent as a voice message on platforms that support it. Returns the file path and duration.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": "The text to convert to speech (max 4000 chars)",
			},
			"model": map[string]any{
				"type":        "string",
				"description": "TTS model: 'speech-01' (default), 'speech-01-turbo', or 'speech-02'",
				"default":     "speech-01",
			},
			"voice": map[string]any{
				"type":        "string",
				"description": "Voice preset: 'male-qnqikwn', 'female-qinghua' (default)",
				"default":     "female-qinghua",
			},
			"output_path": map[string]any{
				"type":        "string",
				"description": "Output file path (default: ~/voice-memos/<timestamp>.mp3)",
			},
			"speed": map[string]any{
				"type":        "number",
				"description": "Speech speed multiplier (0.5 to 2.0, default 1.0)",
				"default":     1.0,
			},
		},
		"required": []any{"text"},
	},
}

func textToSpeechHandler(args map[string]any) string {
	text, _ := args["text"].(string)
	if text == "" {
		return toolError("text_to_speech requires 'text' argument")
	}
	if len(text) > 4000 {
		text = text[:4000]
	}

	model := "speech-01"
	if m, ok := args["model"].(string); ok && m != "" {
		model = m
	}

	voice := "female-qinghua"
	if v, ok := args["voice"].(string); ok && v != "" {
		voice = v
	}

	speed := 1.0
	if s, ok := args["speed"].(float64); ok {
		speed = s
	}

	outputPath := os.ExpandEnv("~/voice-memos/")
	if p, ok := args["output_path"].(string); ok && p != "" {
		outputPath = os.ExpandEnv(p)
	}

	// If output is a dir, generate filename
	if strings.HasSuffix(outputPath, "/") || !strings.Contains(outputPath, ".") {
		if err := os.MkdirAll(outputPath, 0755); err != nil {
			return toolError(fmt.Sprintf("failed to create output directory: %v", err))
		}
		timestamp := time.Now().Format("20060102_150405")
		outputPath = filepath.Join(outputPath, timestamp+".mp3")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	apiKey := os.Getenv("MINIMAX_API_KEY")
	if apiKey == "" {
		cfg, err := config.Load()
		if err == nil && cfg.APIKeys != nil {
			if k, ok := cfg.APIKeys["minimax"]; ok {
				apiKey = k
			}
		}
	}
	if apiKey == "" {
		return toolError("MINIMAX_API_KEY not set — set it in ~/.hermes/config.yaml or environment")
	}

	reqBody := map[string]any{
		"model":   model,
		"text":    text,
		"stream":  false,
		"voice_setting": map[string]any{
			"voice_id": voice,
			"speed":    speed,
		},
		"output_format": map[string]any{
			"format": "mp3",
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return toolError(fmt.Sprintf("failed to marshal request: %v", err))
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.minimaxi.com/v1/audio/t2a", bytes.NewReader(bodyBytes))
	if err != nil {
		return toolError(fmt.Sprintf("failed to create request: %v", err))
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return toolError(fmt.Sprintf("request failed: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return toolError(fmt.Sprintf("TTS API status %d: %s", resp.StatusCode, string(respBody)))
	}

	// MiniMax TTS returns audio binary directly
	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return toolError(fmt.Sprintf("failed to read audio data: %v", err))
	}

	absPath, err := filepath.Abs(outputPath)
	if err != nil {
		return toolError(fmt.Sprintf("invalid output path: %v", err))
	}
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return toolError(fmt.Sprintf("failed to create output directory: %v", err))
	}
	if err := os.WriteFile(absPath, audioData, 0644); err != nil {
		return toolError(fmt.Sprintf("failed to write audio file: %v", err))
	}

	// Estimate duration: ~128kbps MP3 = 16KB/sec
	estDuration := float64(len(audioData)) / 16000.0

	return toolResultData(map[string]any{
		"path":       absPath,
		"size_bytes": len(audioData),
		"format":     "mp3",
		"duration_s": fmt.Sprintf("%.1fs", estDuration),
		"text_chars": len(text),
		"model":      model,
		"voice":      voice,
	})
}

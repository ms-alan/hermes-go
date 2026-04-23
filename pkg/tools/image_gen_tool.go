package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nousresearch/hermes-go/config"
)

// --------------------------------------------------------------------------
// image_generate — generate images from text prompts
// --------------------------------------------------------------------------

var imageGenerateSchema = map[string]any{
	"name":        "image_generate",
	"description": "Generate an image from a text prompt using MiniMax image generation API. The image is saved to a file and returned as a base64-encoded data URI. Requires MINIMAX_API_KEY or configured api_keys.minimax.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "Text description of the image to generate",
			},
			"model": map[string]any{
				"type":        "string",
				"description": "Image model: 'image-01' (default) or 'image-01-preview'",
				"default":     "image-01",
			},
			"output_path": map[string]any{
				"type":        "string",
				"description": "Output file path (default: ~/Downloads/hermes_image.png)",
			},
		},
		"required": []any{"prompt"},
	},
}

func imageGenerateHandler(args map[string]any) string {
	prompt, _ := args["prompt"].(string)
	if prompt == "" {
		return toolError("image_generate requires 'prompt' argument")
	}

	model := "image-01"
	if m, ok := args["model"].(string); ok && m != "" {
		model = m
	}

	outputPath := "~/Downloads/hermes_image.png"
	if p, ok := args["output_path"].(string); ok && p != "" {
		outputPath = p
	}
	outputPath = os.ExpandEnv(outputPath)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
		"model":    model,
		"prompt":   prompt,
		"num_images": 1,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return toolError(fmt.Sprintf("failed to marshal request: %v", err))
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.minimaxi.com/v1/images/generations", bytes.NewReader(bodyBytes))
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

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return toolError(fmt.Sprintf("failed to read response: %v", err))
	}

	if resp.StatusCode != http.StatusOK {
		return toolError(fmt.Sprintf("image gen API status %d: %s", resp.StatusCode, string(respBody)))
	}

	var genResult struct {
		Data []struct {
			URL        string `json:"url"`
			Base64Data string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &genResult); err != nil {
		return toolError(fmt.Sprintf("failed to parse response: %v", err))
	}

	if len(genResult.Data) == 0 {
		return toolError("no image returned from API")
	}

	img := genResult.Data[0]

	// Save to file
	absPath, err := filepath.Abs(outputPath)
	if err != nil {
		return toolError(fmt.Sprintf("invalid output path: %v", err))
	}
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return toolError(fmt.Sprintf("failed to create output directory: %v", err))
	}

	var imgData []byte
	if img.Base64Data != "" {
		imgData, _ = b64Decode(img.Base64Data)
	}

	if len(imgData) == 0 && img.URL != "" {
		// Fetch from URL
		imgData, err = fetchImage(ctx, img.URL)
		if err != nil {
			return toolError(fmt.Sprintf("failed to fetch image from URL: %v", err))
		}
	}

	if len(imgData) > 0 {
		if err := os.WriteFile(absPath, imgData, 0644); err != nil {
			return toolError(fmt.Sprintf("failed to write image: %v", err))
		}
	}

	return toolResultData(map[string]any{
		"path":        absPath,
		"prompt":      prompt,
		"model":       model,
		"size_bytes":  len(imgData),
		"description": fmt.Sprintf("Image generated and saved to %s (%d bytes)", absPath, len(imgData)),
	})
}

func b64Decode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

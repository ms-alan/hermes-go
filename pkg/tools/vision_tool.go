package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nousresearch/hermes-go/config"
)

// --------------------------------------------------------------------------
// vision_analyze — analyze images using AI vision
// --------------------------------------------------------------------------

var visionSchema = map[string]any{
	"name":        "vision_analyze",
	"description": "Analyze an image using AI vision. Accepts a local file path, HTTP/HTTPS URL, or base64-encoded image data. Returns a detailed description and answers your specific question about the image content.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"image_url": map[string]any{
				"type":        "string",
				"description": "Image URL (http/https), local file path, or base64 data URI",
			},
			"question": map[string]any{
				"type":        "string",
				"description": "Your specific question about the image",
			},
		},
		"required": []any{"image_url", "question"},
	},
}

func visionAnalyzeHandler(args map[string]any) string {
	imageURL, _ := args["image_url"].(string)
	if imageURL == "" {
		return toolError("vision_analyze requires 'image_url' argument")
	}
	question, _ := args["question"].(string)
	if question == "" {
		return toolError("vision_analyze requires 'question' argument")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Load image data
	var imageData []byte
	var err error

	switch {
	case len(imageURL) > 5 && imageURL[:5] == "data:":
		// base64 data URI: data:image/png;base64,XXXXX
		idx := 0
		for i, c := range imageURL {
			if c == ',' {
				idx = i
				break
			}
		}
		if idx > 0 {
			imageData, err = base64.StdEncoding.DecodeString(imageURL[idx+1:])
			if err != nil {
				return toolError(fmt.Sprintf("failed to decode base64 image: %v", err))
			}
		}
	case len(imageURL) > 8 && (imageURL[:7] == "http://" || imageURL[:8] == "https://"):
		imageData, err = fetchImage(ctx, imageURL)
		if err != nil {
			return toolError(fmt.Sprintf("failed to fetch image: %v", err))
		}
	default:
		// Local file
		absPath, err := filepath.Abs(os.ExpandEnv(imageURL))
		if err != nil {
			return toolError(fmt.Sprintf("invalid path: %v", err))
		}
		imageData, err = os.ReadFile(absPath)
		if err != nil {
			return toolError(fmt.Sprintf("failed to read image file: %v", err))
		}
	}

	if len(imageData) == 0 {
		return toolError("no image data loaded")
	}

	// Call MiniMax vision API
	result, err := callVisionAPI(ctx, imageData, question)
	if err != nil {
		return toolError(fmt.Sprintf("vision API call failed: %v", err))
	}

	return toolResultData(map[string]any{
		"description": result,
		"image_size":  len(imageData),
	})
}

func fetchImage(ctx context.Context, imageURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", imageURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func callVisionAPI(ctx context.Context, imageData []byte, question string) (string, error) {
	apiKey := os.Getenv("MINIMAX_API_KEY")
	if apiKey == "" {
		// Fall back to config-based key loading
		apiKey = getAPIKeyFromConfig()
	}
	if apiKey == "" {
		return "", fmt.Errorf("MINIMAX_API_KEY not set")
	}

	// Build multipart form request for MiniMax vision
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Image field
	part, err := writer.CreateFormFile("image", "image.jpg")
	if err != nil {
		return "", err
	}
	part.Write(imageData)

	// Parameters
	_ = writer.WriteField("model", "MiniMax-Text-01")
	_ = writer.WriteField("question", question)

	// Extra fields MiniMax vision API requires
	_ = writer.WriteField("timeout", "60")

	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.minimaxi.com/v1/images/query", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vision API status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse MiniMax vision response — extract "description" or "response" field
	var result struct {
		Description string `json:"description"`
		Response    string `json:"response"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		// Fall back to raw text
		text := string(respBody)
		if len(text) > 500 {
			text = text[:500]
		}
		return text, nil
	}
	if result.Description != "" {
		return result.Description, nil
	}
	if result.Response != "" {
		return result.Response, nil
	}
	return string(respBody), nil
}

// getAPIKeyFromConfig loads API key from config file.
func getAPIKeyFromConfig() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	if cfg.APIKeys != nil {
		if k, ok := cfg.APIKeys["minimax"]; ok {
			return k
		}
		if k, ok := cfg.APIKeys["openai"]; ok {
			return k
		}
	}
	return ""
}



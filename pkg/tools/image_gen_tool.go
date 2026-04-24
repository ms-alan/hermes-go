package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ============================================================================
// Image Generation Tool — generate images via FAL.ai API
// ============================================================================
//
// Requires FAL_KEY environment variable (FAL.ai API key).
// Models: flux.2, flux.3, flux.3-pro, flux-dev, flux-schnell, etc.
// Supports aspect ratios: 1:1, 16:9, 9:16, 4:3, 3:4, 21:9, 9:21
//
// Also supports Redux image-to-image via FAL_REDUX_KEY.

const falAPIURL = "https://queue.fal.run/fal-ai/flux.3"

var falModels = map[string]string{
	"flux.3-pro":      "fal-ai/flux-3/pro",
	"flux.3":         "fal-ai/flux-3",
	"flux.2":         "fal-ai/flux.2",
	"flux-dev":       "fal-ai/flux-dev",
	"flux-schnell":   "fal-ai/flux-schnell",
	"flux.3-fast":    "fal-ai/flux-3/fast",
	"flux-pro":       "fal-ai/flux-pro",
	"flux-Realism":   "fal-ai/flux-realism",
	"dall-e-3":       "openai/dall-e-3",
	"sdxl-lightning": "fal-ai/sdxl-lightning",
}

var aspectRatios = map[string]string{
	"1:1":   "square_hd",
	"1:1h":  "square",
	"16:9":  "landscape_16_9",
	"9:16":  "portrait_16_9",
	"4:3":   "landscape_4_3",
	"3:4":   "portrait_4_3",
	"21:9":  "landscape_21_9",
	"9:21":  "portrait_9_16",
	"16:9h": "landscape_wide",
}

var imageExtensions = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true}

func imageGenCheck() bool {
	return os.Getenv("FAL_KEY") != "" || os.Getenv("FAL_API_KEY") != ""
}

var imageGenSchema = map[string]any{
	"name":        "image_generate",
	"description": "Generate images from text prompts using FAL.ai API. Supports multiple models (FLUX.3, FLUX.2, SDXL, DALL-E 3, etc.) and aspect ratios. Returns image URL. Optionally save to a local path.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "Detailed text description of the image to generate. Be specific about subjects, style, lighting, composition.",
			},
			"model": map[string]any{
				"type":        "string",
				"description": fmt.Sprintf("Model to use. Options: %v. Default: flux.3", getMapKeys(falModels)),
				"default":     "flux.3",
			},
			"aspect_ratio": map[string]any{
				"type":        "string",
				"description": "Aspect ratio: 1:1, 16:9, 9:16, 4:3, 3:4, 21:9, 9:21. Default: 1:1",
				"default":     "1:1",
			},
			"save_to": map[string]any{
				"type":        "string",
				"description": "Optional local file path to download and save the generated image.",
			},
			"num_images": map[string]any{
				"type":        "integer",
				"description": "Number of images to generate (1-4). Default: 1",
				"default":     1,
			},
			"style": map[string]any{
				"type":        "string",
				"description": "Style preset: photorealistic, anime, art, logo, product, fashion. Optional.",
			},
			"guidance_scale": map[string]any{
				"type":        "number",
				"description": "Guidance scale (0-10). Higher = more prompt adherence, less creativity. Default: 3.5",
			},
			"num_inference_steps": map[string]any{
				"type":        "integer",
				"description": "Inference steps. More = higher quality but slower. Default: 50",
			},
			"seed": map[string]any{
				"type":        "integer",
				"description": "Random seed for reproducibility. Same seed + prompt = same image.",
			},
			"negative_prompt": map[string]any{
				"type":        "string",
				"description": "Things to avoid in the image.",
			},
		},
		"required": []any{"prompt"},
	},
}

func getMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

var imageGenClient = &http.Client{Timeout: 120 * time.Second}

func imageGenHandler(args map[string]any) string {
	prompt, _ := args["prompt"].(string)
	if prompt = strings.TrimSpace(prompt); prompt == "" {
		return toolError("prompt is required")
	}

	modelKey := imgStr(args, "model", "flux.3")
	modelName, ok := falModels[modelKey]
	if !ok {
		return toolError(fmt.Sprintf("unknown model %q. Available: %v", modelKey, getMapKeys(falModels)))
	}

	aspectStr := imgStr(args, "aspect_ratio", "1:1")
	sizePreset, ok := aspectRatios[aspectStr]
	if !ok {
		return toolError(fmt.Sprintf("unknown aspect ratio %q. Available: 1:1, 16:9, 9:16, 4:3, 3:4, 21:9, 9:21", aspectStr))
	}

	saveTo := imgStr(args, "save_to", "")
	numImages := imgInt(args, "num_images", 1)
	if numImages < 1 {
		numImages = 1
	}
	if numImages > 4 {
		numImages = 4
	}

	// Build FAL payload
	payload := map[string]any{
		"prompt":       prompt,
		"image_size":   sizePreset,
		"num_images":   numImages,
	}

	// Optional fields
	if style := imgStr(args, "style", ""); style != "" {
		payload["style"] = style
	}
	if guidance := imgFloat(args, "guidance_scale", 0); guidance > 0 {
		payload["guidance_scale"] = guidance
	}
	if steps := imgInt(args, "num_inference_steps", 0); steps > 0 {
		payload["num_inference_steps"] = steps
	}
	if seed := imgInt(args, "seed", 0); seed > 0 {
		payload["seed"] = seed
	}
	if neg := imgStr(args, "negative_prompt", ""); neg != "" {
		payload["negative_prompt"] = neg
	}

	// Get API key
	apiKey := os.Getenv("FAL_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("FAL_API_KEY")
	}
	if apiKey == "" {
		return toolError("FAL_KEY or FAL_API_KEY environment variable must be set")
	}

	// Submit request
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return toolError("failed to marshal request: " + err.Error())
	}

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("https://queue.fal.run/%s", modelName), bytes.NewReader(bodyBytes))
	if err != nil {
		return toolError("failed to create request: " + err.Error())
	}
	req.Header.Set("Authorization", "Key "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := imageGenClient.Do(req)
	if err != nil {
		return toolError("FAL API request failed: " + err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return toolError("FAL API authentication failed — check your FAL_KEY")
	}
	if resp.StatusCode == 402 {
		return toolError("FAL API insufficient credits")
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return toolError(fmt.Sprintf("FAL API error %d: %s", resp.StatusCode, string(body)))
	}

	var falResp struct {
		Images []struct {
			URL      string `json:"url"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			Seed     int64  `json:"seed"`
		} `json:"images"`
		Seed    int64  `json:"seed"`
		Timing  any    `json:"timing"`
		RequestID string `json:"request_id"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&falResp); err != nil {
		return toolError("failed to parse FAL response: " + err.Error())
	}

	type ImageOut struct {
		URL       string `json:"url"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Seed      int64  `json:"seed"`
		LocalPath string `json:"local_path,omitempty"`
	}
	results := make([]ImageOut, len(falResp.Images))
	for i, img := range falResp.Images {
		results[i] = ImageOut{URL: img.URL, Width: img.Width, Height: img.Height, Seed: img.Seed}
	}

	// Optionally download and save images
	if saveTo != "" && len(results) > 0 {
		imgURL := results[0].URL
		if err := downloadImage(imgURL, saveTo); err != nil {
			results[0].LocalPath = "download failed: " + err.Error()
		} else {
			results[0].LocalPath = saveTo
		}
	}

	return toolResultData(map[string]any{
		"success":     true,
		"model":       modelKey,
		"aspect_ratio": aspectStr,
		"images":      results,
		"request_id":  falResp.RequestID,
	})
}

// downloadImage downloads an image from a URL to a local path.
func downloadImage(imageURL, destPath string) error {
	req, err := http.NewRequest(http.MethodGet, imageURL, nil)
	if err != nil {
		return err
	}
	resp, err := imageGenClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	// Detect format from content-type if possible
	contentType := resp.Header.Get("Content-Type")
	ext := filepath.Ext(destPath)
	if ext == "" || !imageExtensions[ext] {
		switch {
		case strings.Contains(contentType, "png"):
			if !strings.HasSuffix(destPath, ".png") {
				destPath += ".png"
			}
		case strings.Contains(contentType, "jpeg") || strings.Contains(contentType, "jpg"):
			if !strings.HasSuffix(destPath, ".jpg") {
				destPath += ".jpg"
			}
		case strings.Contains(contentType, "webp"):
			if !strings.HasSuffix(destPath, ".webp") {
				destPath += ".webp"
			}
		}
	}
	return os.WriteFile(destPath, data, 0644)
}

// imgStr helper
func imgStr(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return def
}

// imgInt helper
func imgInt(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	if v, ok := args[key].(int); ok {
		return v
	}
	return def
}

// imgFloat helper
func imgFloat(args map[string]any, key string, def float64) float64 {
	if v, ok := args[key].(float64); ok {
		return v
	}
	if v, ok := args[key].(int); ok {
		return float64(v)
	}
	return def
}

func init() {
	Register("image_generate", "image_gen", imageGenSchema, imageGenHandler, imageGenCheck,
		[]string{"FAL_KEY", "FAL_API_KEY"}, false,
		"Generate images from text prompts via FAL.ai (FLUX.3, DALL-E 3, etc.)", "🎨")
}

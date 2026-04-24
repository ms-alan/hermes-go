package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/net/html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// Web Extract Tool — fetch and extract content from URLs
// ============================================================================
//
// Extracts readable text from web pages via direct HTTP fetch + HTML parsing.
// Optional LLM summarization if AUXILIARY_* variables are configured.
//
// Actions:
// - extract: fetch one or more URLs and return their text content
// - extract_and_summarize: fetch + LLM summarization (if LLM available)
// - search_extract: use Exa API for semantic search + content extraction

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

func webExtractLLMCheck() bool {
	return os.Getenv("AUXILIARY_WEB_EXTRACT_MODEL") != "" ||
		os.Getenv("OPENAI_API_KEY") != "" ||
		os.Getenv("OPENROUTER_API_KEY") != ""
}

// ---------------------------------------------------------------------------
// Tool schema
// ---------------------------------------------------------------------------

var webExtractToolSchema = map[string]any{
	"name":        "web_extract",
	"description": "Extract readable text content from web pages. Fetches one or more URLs and returns cleaned plain text. Optionally use extract_and_summarize for LLM-powered summarization of long content.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action: extract (default) or extract_and_summarize",
				"enum":        []any{"extract", "extract_and_summarize"},
				"default":     "extract",
			},
			"urls": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "List of URLs to extract content from",
			},
			"model": map[string]any{
				"type":        "string",
				"description": "LLM model for summarization (default: from AUXILIARY_WEB_EXTRACT_MODEL env or openai gpt-4o-mini)",
			},
		},
		"required": []any{"urls"},
	},
}

// ---------------------------------------------------------------------------
// HTTP client
// ---------------------------------------------------------------------------

var webExtractClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        10,
		IdleConnTimeout:     60 * time.Second,
		DisableKeepAlives:   false,
		TLSHandshakeTimeout: 10 * time.Second,
	},
}

// ---------------------------------------------------------------------------
// HTML text extraction
// ---------------------------------------------------------------------------

// extractTextFromHTML walks the HTML node tree and extracts visible text content.
func extractTextFromHTML(n *html.Node, includeLinks bool) string {
	var buf bytes.Buffer
	extractNode(n, &buf, includeLinks)
	return buf.String()
}

func extractNode(n *html.Node, buf *bytes.Buffer, includeLinks bool) {
	if n.Type == html.TextNode {
		text := strings.TrimSpace(n.Data)
		if text != "" {
			buf.WriteString(text)
			buf.WriteString(" ")
		}
		return
	}

	if n.Type != html.ElementNode {
		return
	}

	tagName := strings.ToLower(n.Data)

	// Block-level elements get a newline before
	blockTags := map[string]bool{
		"p": true, "br": true, "div": true, "h1": true, "h2": true, "h3": true,
		"h4": true, "h5": true, "h6": true, "li": true, "tr": true,
		"blockquote": true, "pre": true, "code": true, "section": true,
		"article": true, "header": true, "footer": true, "nav": true,
		"aside": true, "main": true, "figure": true, "table": true,
		"ul": true, "ol": true, "dl": true, "form": true,
	}

	if blockTags[tagName] {
		buf.WriteString("\n")
	}

	// Skip invisible elements
	invisibleTags := map[string]bool{
		"script": true, "style": true, "noscript": true, "iframe": true,
		"object": true, "embed": true, "svg": true, "canvas": true,
		"head": true, "meta": true, "link": true, "title": true,
	}

	if invisibleTags[tagName] {
		return
	}

	// Extract link text if requested
	if includeLinks && tagName == "a" {
		buf.WriteString("[")
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extractNode(c, buf, includeLinks)
	}

	if includeLinks && tagName == "a" {
		href := getAttr(n, "href")
		if href != "" {
			buf.WriteString(fmt.Sprintf("](%s)", href))
		}
		buf.WriteString("]")
	}

	if blockTags[tagName] {
		buf.WriteString("\n")
	}
}

func getAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if strings.ToLower(attr.Key) == key {
			return attr.Val
		}
	}
	return ""
}

// cleanText post-processes extracted text: collapses whitespace, removes too-short lines.
func webCleanText(text string) string {
	// Replace multiple whitespace with single space
	spaceRe := regexp.MustCompile(`[ \t]+`)
	text = spaceRe.ReplaceAllString(text, " ")

	// Replace 3+ newlines with double newline
	newlineRe := regexp.MustCompile(`\n{3,}`)
	text = newlineRe.ReplaceAllString(text, "\n\n")

	// Remove lines that are too short (likely artifacts)
	lines := strings.Split(text, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) < 3 {
			continue
		}
		cleaned = append(cleaned, line)
	}

	return strings.Join(cleaned, "\n")
}

// fetchAndExtract fetches a URL and returns extracted text.
func fetchAndExtract(targetURL string, includeLinks bool) (string, string, error) {
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Hermes/1.0; +https://github.com/NousResearch/hermes-go)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := webExtractClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "html") && !strings.Contains(contentType, "text") {
		return "", "", fmt.Errorf("not HTML content: %s", contentType)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024)) // 5MB max
	if err != nil {
		return "", "", fmt.Errorf("read body: %w", err)
	}

	// Parse HTML
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("HTML parse failed: %w", err)
	}

	title := extractTitle(doc)
	text := webCleanText(extractTextFromHTML(doc, includeLinks))

	return title, text, nil
}

func extractTitle(n *html.Node) string {
	if n.Type == html.ElementNode && strings.ToLower(n.Data) == "title" && n.FirstChild != nil {
		return strings.TrimSpace(n.FirstChild.Data)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if title := extractTitle(c); title != "" {
			return title
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// LLM summarization
// ---------------------------------------------------------------------------

// summarizeWithLLM sends content to an LLM for summarization.
// Uses OPENAI_API_KEY or AUXILIARY_WEB_EXTRACT_MODEL.
func summarizeWithLLM(content, model, targetURL string) string {
	apiKey := os.Getenv("OPENAI_API_KEY")
	baseURL := "https://api.openai.com/v1"
	modelName := model

	if modelName == "" {
		modelName = os.Getenv("AUXILIARY_WEB_EXTRACT_MODEL")
	}
	if modelName == "" {
		modelName = "gpt-4o-mini"
	}

	if apiKey == "" {
		apiKey = os.Getenv("OPENROUTER_API_KEY")
		baseURL = "https://openrouter.ai/api/v1"
		modelName = "google/gemini-2.0-flash-exp"
	}

	if apiKey == "" {
		return "" // No LLM available, return raw content
	}

	systemPrompt := `You are a web content analyzer. Given raw extracted text from a web page, produce a concise, well-structured markdown summary that captures the key information. Include the most important details, facts, and insights. Do not include filler phrases like "this page contains" or "in this article". Be direct and informative.`

	userPrompt := fmt.Sprintf("Source URL: %s\n\n---\n\n%s\n\n---\n\nProvide a comprehensive summary of this content in markdown format:", targetURL, content)

	payload := map[string]any{
		"model": modelName,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"max_tokens": 2000,
		"temperature": 0.3,
	}

	payloadBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(payloadBytes))
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}

	if len(result.Choices) > 0 {
		return result.Choices[0].Message.Content
	}
	return ""
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func webExtractHandler(args map[string]any) string {
	action, _ := args["action"].(string)
	if action == "" {
		action = "extract"
	}

	urlsRaw, ok := args["urls"]
	if !ok {
		return toolError("urls is required")
	}
	urlList, ok := urlsRaw.([]any)
	if !ok {
		return toolError("urls must be a list")
	}
	if len(urlList) == 0 {
		return toolError("urls list cannot be empty")
	}

	// Parse URLs
	var urls []string
	for _, u := range urlList {
		if us, ok := u.(string); ok {
			parsed, err := url.Parse(us)
			if err == nil && parsed.Scheme != "" && parsed.Host != "" {
				urls = append(urls, us)
			}
		}
	}
	if len(urls) == 0 {
		return toolError("no valid URLs provided")
	}

	// Cap at 10 URLs
	if len(urls) > 10 {
		urls = urls[:10]
	}

	model, _ := args["model"].(string)
	includeLinks := true

	type URLResult struct {
		URL       string `json:"url"`
		Title     string `json:"title,omitempty"`
		Content   string `json:"content,omitempty"`
		Summary   string `json:"summary,omitempty"`
		Error     string `json:"error,omitempty"`
		Status    string `json:"status"`
	}

	results := make([]URLResult, len(urls))

	if action == "extract_and_summarize" {
		// Parallel fetch + summarize
		var wg sync.WaitGroup
		var mu sync.Mutex
		for i, targetURL := range urls {
			wg.Add(1)
			go func(idx int, u string) {
				defer wg.Done()
				title, content, err := fetchAndExtract(u, includeLinks)
				r := URLResult{URL: u, Status: "success"}
				if err != nil {
					r.Status = "error"
					r.Error = err.Error()
				} else {
					r.Title = title
					r.Content = truncateContent(content, 8000)
					// Only summarize if content is long enough
					if len(content) > 2000 {
						r.Summary = summarizeWithLLM(content, model, u)
					}
				}
				mu.Lock()
				results[idx] = r
				mu.Unlock()
			}(i, targetURL)
		}
		wg.Wait()
	} else {
		// Simple parallel fetch
		var wg sync.WaitGroup
		var mu sync.Mutex
		for i, targetURL := range urls {
			wg.Add(1)
			go func(idx int, u string) {
				defer wg.Done()
				title, content, err := fetchAndExtract(u, includeLinks)
				r := URLResult{URL: u, Status: "success"}
				if err != nil {
					r.Status = "error"
					r.Error = err.Error()
				} else {
					r.Title = title
					r.Content = truncateContent(content, 12000)
				}
				mu.Lock()
				results[idx] = r
				mu.Unlock()
			}(i, targetURL)
		}
		wg.Wait()
	}

	successCount := 0
	for _, r := range results {
		if r.Status == "success" {
			successCount++
		}
	}

	return toolResultData(map[string]any{
		"success":     true,
		"results":      results,
		"total":        len(results),
		"succeeded":    successCount,
		"action":       action,
	})
}

func truncateContent(content string, maxLen int) string {
	if len(content) <= maxLen {
		return content
	}
	return content[:maxLen] + fmt.Sprintf("\n\n[... content truncated (%d chars total) ...]", len(content))
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

// webExtractToolSchema is referenced by builtin.go's web_extract registration
var _ = webExtractToolSchema // silence unused warning

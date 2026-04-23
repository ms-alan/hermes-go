package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// httpClient is the shared HTTP client (supports proxy via env).
var httpClient = &http.Client{Timeout: 30}

// envGetter reads an environment variable. Exposed for testing.
var envGetter = func(k string) string {
	return os.Getenv(k)
}

func init() {
	// Respect system proxy settings — configure httpClient with proxy transport
	if proxyURL := proxyFromEnv(); proxyURL != "" {
		if pr, err := url.Parse(proxyURL); err == nil {
			httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(pr)}
		}
	}
}

func proxyFromEnv() string {
	envs := []string{"HTTPS_PROXY", "HTTP_PROXY", "http_proxy", "https_proxy"}
	for _, e := range envs {
		if v := envGetter(e); v != "" {
			return v
		}
	}
	return ""
}

// webExtractHandler extracts clean text content from web pages.
func webExtractHandler(args map[string]any) string {
	urlsRaw, _ := args["urls"].([]any)
	if len(urlsRaw) == 0 {
		return toolError("urls is required (list of URLs)")
	}

	var urls []string
	for _, u := range urlsRaw {
		if s, ok := u.(string); ok {
			urls = append(urls, s)
		}
	}

	format, _ := args["format"].(string)
	if format == "" {
		format = "text"
	}

	ctx := context.Background()
	var results []map[string]any

	for _, rawURL := range urls {
		u, err := url.Parse(rawURL)
		if err != nil {
			results = append(results, map[string]any{
				"url":    rawURL,
				"error":  fmt.Sprintf("invalid URL: %v", err),
				"status": "error",
			})
			continue
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			results = append(results, map[string]any{
				"url":    rawURL,
				"error":  "only http/https URLs are supported",
				"status": "error",
			})
			continue
		}

		content, status, err := extractURL(ctx, rawURL)
		if err != nil {
			results = append(results, map[string]any{
				"url":    rawURL,
				"error":  err.Error(),
				"status": status,
			})
			continue
		}

		// Truncate to 8000 chars to save tokens
		if len(content) > 8000 {
			content = content[:8000] + "\n\n[... content truncated at 8000 chars ...]"
		}

		results = append(results, map[string]any{
			"url":         rawURL,
			"status":      status,
			"content":     content,
			"format":      format,
			"char_count":  len(content),
		})
	}

	return toolResultData(map[string]any{
		"extracted": results,
		"count":     len(results),
	})
}

// extractURL fetches a URL and extracts clean text content.
func extractURL(ctx context.Context, rawURL string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", "error", fmt.Errorf("bad URL: %w", err)
	}

	proxyURL := proxyFromEnv()
	var client *http.Client
	if proxyURL != "" {
		if pr, err := url.Parse(proxyURL); err == nil {
			client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pr)}}
		}
	} else {
		client = httpClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", "error", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Sprintf("http_%d", resp.StatusCode), fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/html") && !strings.Contains(contentType, "text/plain") {
		// Try to read anyway
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 500_000)) // max 500KB
	if err != nil {
		return "", "error", fmt.Errorf("read body: %w", err)
	}

	html := string(body)
	text := stripHTML(html)

	// If result is very short, might be a login page or error — add title + metadata
	title := extractTitle(html)
	var result string
	if title != "" {
		result = fmt.Sprintf("Title: %s\nURL: %s\n\n%s", title, rawURL, text)
	} else {
		result = fmt.Sprintf("URL: %s\n\n%s", rawURL, text)
	}

	return result, "ok", nil
}

// stripHTML removes HTML tags and decodes common entities.
func stripHTML(html string) string {
	// Remove script and style blocks
	scriptRe := regexp.MustCompile(`(?is)<(script|style|noscript|iframe)[^>]*>.*?</\1>`)
	html = scriptRe.ReplaceAllString(html, "")

	// Remove HTML comments
	html = regexp.MustCompile(`<!--.*?-->`).ReplaceAllString(html, "")

	// Remove all HTML tags
	html = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(html, "\n")

	// Decode entities
	html = decodeHTMLEntities(html)

	// Collapse whitespace
	lines := strings.Split(html, "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) > 0 {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func decodeHTMLEntities(s string) string {
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&apos;", "'")
	return s
}

func extractTitle(html string) string {
	m := regexp.MustCompile(`(?is)<title[^>]*>([^<]+)</title>`)
	match := m.FindStringSubmatch(html)
	if len(match) > 1 {
		return strings.TrimSpace(decodeHTMLEntities(match[1]))
	}
	return ""
}


package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// WebSearchSchema is the tool schema for web_search.
var WebSearchSchema = map[string]any{
	"name":        "web_search",
	"description": "Search the web for information. Auto-selects the best available backend: Firecrawl (default, most capable), Parallel, Tavily, or Exa. Set FIRECRAWL_API_KEY, PARALLEL_API_KEY, TAVILY_API_KEY, or EXA_API_KEY to enable.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query to look up on the web",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results to return (default: 5, max: 20)",
				"default":     5,
			},
		},
		"required": []any{"query"},
	},
}

// WebSearchHandler is the tool handler for web_search.
func WebSearchHandler(args map[string]any) string {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return toolError("web_search requires a 'query' argument")
	}

	limit := 5
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 20 {
		limit = 20
	}

	result := doWebSearch(query, limit)

	resp := map[string]any{
		"query":   result.Query,
		"results": result.Results,
		"count":   result.Count,
	}
	if result.Backend != "" {
		resp["backend"] = string(result.Backend)
	}
	if result.Info != "" {
		resp["info"] = result.Info
	}
	if result.Error != "" {
		return toolError(result.Error)
	}

	return toolResultData(resp)
}

// CheckWebSearch returns true if any web search backend is configured.
func CheckWebSearch() bool {
	b := getWebSearchBackend()
	return isWebSearchBackendAvailable(b)
}

// webSearchBackend represents a supported web search provider.
type webSearchBackend string

const (
	backendFirecrawl webSearchBackend = "firecrawl"
	backendParallel  webSearchBackend = "parallel"
	backendTavily    webSearchBackend = "tavily"
	backendExa       webSearchBackend = "exa"
)

// webSearchResult represents a single search result.
type webSearchResult struct {
	Position    int    `json:"position"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// webSearchResponse is the normalized response for web search.
type webSearchResponse struct {
	Query    string             `json:"query"`
	Results  []webSearchResult  `json:"results"`
	Count    int                `json:"count"`
	Backend  webSearchBackend   `json:"backend,omitempty"`
	Info     string             `json:"info,omitempty"`
	Error    string             `json:"error,omitempty"`
}

// getWebSearchBackend determines which backend to use based on available API keys.
// Priority: Firecrawl > Parallel > Tavily > Exa
func getWebSearchBackend() webSearchBackend {
	// Check Firecrawl
	if os.Getenv("FIRECRAWL_API_KEY") != "" || os.Getenv("FIRECRAWL_API_URL") != "" {
		return backendFirecrawl
	}
	// Check Parallel
	if os.Getenv("PARALLEL_API_KEY") != "" {
		return backendParallel
	}
	// Check Tavily
	if os.Getenv("TAVILY_API_KEY") != "" {
		return backendTavily
	}
	// Check Exa
	if os.Getenv("EXA_API_KEY") != "" {
		return backendExa
	}
	// Default to Firecrawl (will return "not configured" below)
	return backendFirecrawl
}

// isWebSearchBackendAvailable checks if a specific backend has credentials.
func isWebSearchBackendAvailable(b webSearchBackend) bool {
	switch b {
	case backendFirecrawl:
		return os.Getenv("FIRECRAWL_API_KEY") != "" || os.Getenv("FIRECRAWL_API_URL") != ""
	case backendParallel:
		return os.Getenv("PARALLEL_API_KEY") != ""
	case backendTavily:
		return os.Getenv("TAVILY_API_KEY") != ""
	case backendExa:
		return os.Getenv("EXA_API_KEY") != ""
	}
	return false
}

// doWebSearch is the main entry point, dispatching to the appropriate backend.
func doWebSearch(query string, limit int) webSearchResponse {
	backend := getWebSearchBackend()

	// Return "not configured" error if no backend is available
	if !isWebSearchBackendAvailable(backend) {
		return webSearchResponse{
			Query:   query,
			Results: []webSearchResult{{
				Title:       "Web search not configured",
				URL:         "",
				Description: "No web search API key configured. Set FIRECRAWL_API_KEY, PARALLEL_API_KEY, TAVILY_API_KEY, or EXA_API_KEY in ~/.hermes/.env. Priority: Firecrawl > Parallel > Tavily > Exa.",
			}},
			Count:   1,
			Backend: backend,
			Info:    "Set one of FIRECRAWL_API_KEY, PARALLEL_API_KEY, TAVILY_API_KEY, or EXA_API_KEY in environment to enable web search",
		}
	}

	switch backend {
	case backendFirecrawl:
		return searchFirecrawl(query, limit)
	case backendParallel:
		return searchParallel(query, limit)
	case backendTavily:
		return searchTavily(query, limit)
	case backendExa:
		return searchExa(query, limit)
	}

	return webSearchResponse{Query: query, Error: "unknown backend"}
}

// ---------------------------------------------------------------------------
// Firecrawl
// ---------------------------------------------------------------------------

func searchFirecrawl(query string, limit int) webSearchResponse {
	apiURL := os.Getenv("FIRECRAWL_API_URL")
	if apiURL == "" {
		apiURL = "https://api.firecrawl.dev"
	}
	apiURL = strings.TrimSuffix(apiURL, "/") + "/v0/search"

	apiKey := os.Getenv("FIRECRAWL_API_KEY")

	payload := map[string]any{
		"query": query,
		"limit": limit,
	}
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("marshal error: %v", err)}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("request error: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("Firecrawl request failed: %v", err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("read error: %v", err)}
	}

	if resp.StatusCode != http.StatusOK {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("Firecrawl API status %d: %s", resp.StatusCode, string(body))}
	}

	// Firecrawl response: { "data": { "web": [ { "url", "title", "description" } ] } }
	var raw struct {
		Data struct {
			Web []struct {
				URL         string `json:"url"`
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"web"`
			Results []struct {
				URL         string `json:"url"`
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		// Try alternate shape
		var alt struct {
			Data []struct {
				URL         string `json:"url"`
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"data"`
		}
		if uerr := json.Unmarshal(body, &alt); uerr != nil {
			return webSearchResponse{Query: query, Error: fmt.Sprintf("parse error: %v", err)}
		}
		results := make([]webSearchResult, 0, len(alt.Data))
		for i, r := range alt.Data {
			results = append(results, webSearchResult{
				Position:    i + 1,
				Title:       r.Title,
				URL:         r.URL,
				Description: truncate(r.Description, 300),
			})
		}
		return webSearchResponse{Query: query, Results: results, Count: len(results), Backend: backendFirecrawl}
	}

	webData := raw.Data.Web
	if len(webData) == 0 {
		webData = raw.Data.Results
	}

	results := make([]webSearchResult, 0, len(webData))
	for i, r := range webData {
		results = append(results, webSearchResult{
			Position:    i + 1,
			Title:       r.Title,
			URL:         r.URL,
			Description: truncate(r.Description, 300),
		})
	}
	return webSearchResponse{Query: query, Results: results, Count: len(results), Backend: backendFirecrawl}
}

// ---------------------------------------------------------------------------
// Parallel
// ---------------------------------------------------------------------------

func searchParallel(query string, limit int) webSearchResponse {
	apiKey := os.Getenv("PARALLEL_API_KEY")
	apiURL := "https://api.parallel.ai/v1/search"

	if limit > 20 {
		limit = 20
	}

	payload := map[string]any{
		"search_queries": []string{query},
		"objective":     query,
		"mode":          "agentic",
		"max_results":   limit,
	}
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("marshal error: %v", err)}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("request error: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("Parallel request failed: %v", err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("read error: %v", err)}
	}

	if resp.StatusCode != http.StatusOK {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("Parallel API status %d: %s", resp.StatusCode, string(body))}
	}

	// Parallel response: { "results": [ { "url", "title", "excerpts": [] } ] }
	var raw struct {
		Results []struct {
			URL      string   `json:"url"`
			Title    string   `json:"title"`
			Excerpts []string `json:"excerpts"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("parse error: %v", err)}
	}

	results := make([]webSearchResult, 0, len(raw.Results))
	for i, r := range raw.Results {
		desc := strings.Join(r.Excerpts, " ")
		results = append(results, webSearchResult{
			Position:    i + 1,
			Title:       r.Title,
			URL:         r.URL,
			Description: truncate(desc, 300),
		})
	}
	return webSearchResponse{Query: query, Results: results, Count: len(results), Backend: backendParallel}
}

// ---------------------------------------------------------------------------
// Tavily (moved from builtin.go)
// ---------------------------------------------------------------------------

func searchTavily(query string, limit int) webSearchResponse {
	apiKey := os.Getenv("TAVILY_API_KEY")

	if limit > 20 {
		limit = 20
	}

	payload := map[string]any{
		"query":           query,
		"api_key":         apiKey,
		"max_results":     limit,
		"search_depth":    "basic",
		"include_answer":  false,
	}
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("marshal error: %v", err)}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.tavily.com/search", bytes.NewReader(reqBody))
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("request error: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("Tavily request failed: %v", err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("read error: %v", err)}
	}

	if resp.StatusCode != http.StatusOK {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("Tavily API status %d: %s", resp.StatusCode, string(body))}
	}

	var raw struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Content     string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("parse error: %v", err)}
	}

	results := make([]webSearchResult, 0, len(raw.Results))
	for i, r := range raw.Results {
		desc := r.Description
		if desc == "" {
			desc = r.Content
		}
		results = append(results, webSearchResult{
			Position:    i + 1,
			Title:       r.Title,
			URL:         r.URL,
			Description: truncate(desc, 300),
		})
	}
	return webSearchResponse{Query: query, Results: results, Count: len(results), Backend: backendTavily}
}

// ---------------------------------------------------------------------------
// Exa
// ---------------------------------------------------------------------------

func searchExa(query string, limit int) webSearchResponse {
	apiKey := os.Getenv("EXA_API_KEY")

	v := url.Values{}
	v.Set("q", query)
	v.Set("num_results", strconv.Itoa(limit))
	v.Set("highlights", "true")
	v.Set("include_answer", "false")

	apiURL := "https://api.exa.ai/search?" + v.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("request error: %v", err)}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-exa-integration", "hermes-go")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("Exa request failed: %v", err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("read error: %v", err)}
	}

	if resp.StatusCode != http.StatusOK {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("Exa API status %d: %s", resp.StatusCode, string(body))}
	}

	// Exa response: { "results": [ { "url", "title", "highlights": [] } ] }
	var raw struct {
		Results []struct {
			URL         string   `json:"url"`
			Title       string   `json:"title"`
			Highlights  []string `json:"highlights"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return webSearchResponse{Query: query, Error: fmt.Sprintf("parse error: %v", err)}
	}

	results := make([]webSearchResult, 0, len(raw.Results))
	for i, r := range raw.Results {
		desc := strings.Join(r.Highlights, " ")
		results = append(results, webSearchResult{
			Position:    i + 1,
			Title:       r.Title,
			URL:         r.URL,
			Description: truncate(desc, 300),
		})
	}
	return webSearchResponse{Query: query, Results: results, Count: len(results), Backend: backendExa}
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

func truncate(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

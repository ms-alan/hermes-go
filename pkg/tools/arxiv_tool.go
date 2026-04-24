package tools

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const arxivEndpoint = "https://export.arxiv.org/api/query"

var arxivSchema = map[string]any{
	"name":        "arxiv",
	"description": "Search arXiv.org for academic papers. Supports keyword search, author search, category filter, and direct ID lookup. Returns title, authors, abstract, categories, dates, and links.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action: search (default) or get (by ID)",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Search keywords (e.g. 'GRPO reinforcement learning'). Used with action=search.",
			},
			"author": map[string]any{
				"type":        "string",
				"description": "Author name (e.g. 'Yann LeCun'). Used with action=search.",
			},
			"category": map[string]any{
				"type":        "string",
				"description": "arXiv category (e.g. 'cs.AI', 'cs.LG', 'stat.ML'). Used with action=search.",
			},
			"ids": map[string]any{
				"type":        "string",
				"description": "arXiv IDs, comma-separated (e.g. '2402.03300' or '2402.03300,2401.12345'). Used with action=get.",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Max papers to return (default 5, max 100).",
			},
			"sort_by": map[string]any{
				"type":        "string",
				"description": "Sort by: relevance (default), date (submittedDate), updated (lastUpdatedDate).",
			},
		},
	},
}

// arXiv Atom feed structures
type arxivFeed struct {
	XMLName   xml.Name    `xml:"feed"`
	Entries   []arxivEntry `xml:"entry"`
	TotalResults string   `xml:"opftotalResults"`
}

type arxivEntry struct {
	Title       string     `xml:"title"`
	ID          string     `xml:"id"`
	Published   string     `xml:"published"`
	Updated     string     `xml:"updated"`
	Summary     string     `xml:"summary"`
	Authors     []arxivAuthor `xml:"author"`
	Categories  []arxivCategory `xml:"category"`
	Links       []arxivLink `xml:"link"`
}

type arxivAuthor struct {
	Name string `xml:"name"`
}

type arxivCategory struct {
	Term string `xml:"term,attr"`
}

type arxivLink struct {
	Title string `xml:"title,attr"`
	Href  string `xml:"href,attr"`
	Type  string `xml:"type,attr"`
}

func arxivHandler(args map[string]any) string {
	action, _ := args["action"].(string)
	if action == "" {
		action = "search"
	}

	switch action {
	case "search":
		return arxivSearch(args)
	case "get":
		return arxivGet(args)
	default:
		return fmt.Sprintf(`{"error": "unknown action: %q, valid actions: search, get"}`, action)
	}
}

func arxivSearch(args map[string]any) string {
	query := strings.TrimSpace(getString(args, "query"))
	author := strings.TrimSpace(getString(args, "author"))
	category := strings.TrimSpace(getString(args, "category"))
	if query == "" && author == "" && category == "" {
		return `{"error": "at least one of query, author, or category is required for search"}`
	}

	maxResults := clampInt(getFloat(args, "max_results"), 1, 100, 5)
	sortBy := getString(args, "sort_by")
	if sortBy == "" {
		sortBy = "relevance"
	}

	params := url.Values{}
	var parts []string
	if query != "" {
		parts = append(parts, "all:"+url.QueryEscape(query))
	}
	if author != "" {
		parts = append(parts, "au:"+url.QueryEscape(author))
	}
	if category != "" {
		parts = append(parts, "cat:"+url.QueryEscape(category))
	}
	params.Set("search_query", strings.Join(parts, "+AND+"))
	params.Set("max_results", fmt.Sprintf("%d", maxResults))

	sortMap := map[string]string{
		"relevance": "relevance",
		"date":      "submittedDate",
		"updated":   "lastUpdatedDate",
	}
	sb := sortMap[sortBy]
	if sb == "" {
		return fmt.Sprintf(`{"error": "sort_by must be one of: relevance, date, updated, got %q"}`, sortBy)
	}
	params.Set("sortBy", sb)
	params.Set("sortOrder", "descending")

	return arxivFetch(arxivEndpoint + "?" + params.Encode())
}

func arxivGet(args map[string]any) string {
	ids := strings.TrimSpace(getString(args, "ids"))
	if ids == "" {
		return `{"error": "ids is required for get action (comma-separated arXiv IDs, e.g. '2402.03300,2401.12345')"}`
	}

	// Remove whitespace around IDs
	idList := strings.Split(ids, ",")
	for i := range idList {
		idList[i] = strings.TrimSpace(idList[i])
	}

	maxResults := clampInt(getFloat(args, "max_results"), 1, 100, len(idList))

	params := url.Values{}
	params.Set("id_list", strings.Join(idList, ","))
	params.Set("max_results", fmt.Sprintf("%d", maxResults))

	return arxivFetch(arxivEndpoint + "?" + params.Encode())
}

func arxivFetch(url string) string {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Sprintf(`{"error": "failed to create request: %s"}`, err.Error())
	}
	req.Header.Set("User-Agent", "HermesGo/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Sprintf(`{"error": "failed to fetch arXiv: %s"}`, err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Sprintf(`{"error": "arXiv API HTTP %d: %s"}`, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Sprintf(`{"error": "failed to read response: %s"}`, err.Error())
	}

	var feed arxivFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return fmt.Sprintf(`{"error": "failed to parse XML: %s"}`, err.Error())
	}

	if len(feed.Entries) == 0 {
		return `{"results": [], "message": "no results found"}`
	}

	// Parse total results
	total := 0
	if feed.TotalResults != "" {
		fmt.Sscanf(feed.TotalResults, "%d", &total)
	}

	results := make([]map[string]any, len(feed.Entries))
	for i, e := range feed.Entries {
		authors := make([]string, len(e.Authors))
		for j, a := range e.Authors {
			authors[j] = a.Name
		}
		categories := make([]string, len(e.Categories))
		for j, c := range e.Categories {
			categories[j] = c.Term
		}

		// Extract version from ID (e.g. /abs/2402.03300v2 → v2)
		arxivID := extractArxivID(e.ID)
		version := extractVersion(e.ID)

		// Find PDF and abstract links
		var absURL, pdfURL string
		for _, l := range e.Links {
			if l.Title == "pdf" {
				pdfURL = l.Href
			} else if l.Type == "text/html" || strings.HasSuffix(l.Href, "/abs/"+arxivID) {
				absURL = l.Href
			}
		}
		if absURL == "" {
			absURL = fmt.Sprintf("https://arxiv.org/abs/%s", arxivID)
		}
		if pdfURL == "" {
			pdfURL = fmt.Sprintf("https://arxiv.org/pdf/%s.pdf", arxivID)
		}

		results[i] = map[string]any{
			"id":          arxivID + version,
			"arxiv_id":    arxivID,
			"version":     version,
			"title":       cleanText(e.Title),
			"summary":     cleanText(e.Summary),
			"authors":     authors,
			"categories":  categories,
			"published":   trimDate(e.Published),
			"updated":     trimDate(e.Updated),
			"abs_url":     absURL,
			"pdf_url":     pdfURL,
		}
	}

	out := map[string]any{
		"total":   total,
		"returned": len(results),
		"results":  results,
	}
	return toJSON(out)
}

func extractArxivID(rawID string) string {
	// Format: https://arxiv.org/abs/2402.03300v2
	if idx := strings.LastIndex(rawID, "/abs/"); idx >= 0 {
		id := rawID[idx+5:]
		// Strip version
		if vIdx := strings.Index(id, "v"); vIdx > 0 {
			id = id[:vIdx]
		}
		return id
	}
	// Fallback: strip version from raw ID
	re := regexp.MustCompile(`v\d+$`)
	return re.ReplaceAllString(rawID, "")
}

func extractVersion(rawID string) string {
	if idx := strings.LastIndex(rawID, "/abs/"); idx >= 0 {
		id := rawID[idx+5:]
		if vIdx := strings.Index(id, "v"); vIdx > 0 {
			return id[vIdx:]
		}
	}
	return ""
}

func cleanText(s string) string {
	// Normalize whitespace in titles and abstracts
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, " \n ", " ")
	s = strings.TrimSpace(s)
	return s
}

func trimDate(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func getString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func getFloat(args map[string]any, key string) float64 {
	if v, ok := args[key].(float64); ok {
		return v
	}
	return 0
}

func clampInt(v float64, min, def, max int) int {
	if v == 0 {
		return def
	}
	i := int(v)
	if i < min {
		return min
	}
	if i > max {
		return max
	}
	return i
}

func toJSON(m map[string]any) string {
	// Simple JSON marshaler that handles arrays properly
	return jsonMarshal(m)
}

func jsonMarshal(m map[string]any) string {
	var sb strings.Builder
	sb.WriteString("{")
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(fmt.Sprintf("%q:", k))
		switch v := m[k].(type) {
		case string:
			sb.WriteString(fmt.Sprintf("%q", v))
		case int:
			sb.WriteString(fmt.Sprintf("%d", v))
		case float64:
			sb.WriteString(fmt.Sprintf("%g", v))
		case []string:
			sb.WriteString("[")
			for j, s := range v {
				if j > 0 {
					sb.WriteString(",")
				}
				sb.WriteString(fmt.Sprintf("%q", s))
			}
			sb.WriteString("]")
		case []map[string]any:
			sb.WriteString("[")
			for j, item := range v {
				if j > 0 {
					sb.WriteString(",")
				}
				sb.WriteString(jsonMarshal(item))
			}
			sb.WriteString("]")
		default:
			sb.WriteString("null")
		}
	}
	sb.WriteString("}")
	return sb.String()
}

func init() {
	Register("arxiv", "productivity",
		arxivSchema,
		func(args map[string]any) string { return arxivHandler(args) },
		func() bool { return true }, // no API key needed
		[]string{},                 // no env vars
		false,                      // isAsync
		"arXiv paper search — keyword, author, category search + ID lookup",
		"📄",
	)
}

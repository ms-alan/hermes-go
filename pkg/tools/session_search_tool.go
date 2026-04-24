package tools

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nousresearch/hermes-go/pkg/session"
)

var sessionSearchSchema = map[string]any{
	"name":        "session_search",
	"description": "Search past conversation sessions using full-text search. Finds sessions matching keywords, returns snippets grouped by session with metadata. Use without a query to list recent sessions.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query — supports FTS5 syntax: keywords, phrases ('\"exact phrase\"'), boolean ('docker OR kubernetes'), prefix ('deploy*'). Omit to list recent sessions.",
			},
			"role_filter": map[string]any{
				"type":        "string",
				"description": "Filter by message role: 'user' or 'assistant'. Optional.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of sessions to return (default: 3, max: 10)",
			},
		},
	},
}

// sessionSearchHandler handles session search requests.
func sessionSearchHandler(args map[string]any) string {
	query, _ := args["query"].(string)
	roleFilter, _ := args["role_filter"].(string)
	limit := 3
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
		if limit <= 0 {
			limit = 3
		}
		if limit > 10 {
			limit = 10
		}
	}

	store, err := session.NewStore()
	if err != nil {
		return toolError(fmt.Sprintf("failed to open session store: %v", err))
	}
	defer store.Close()

	// No query → list recent sessions
	if strings.TrimSpace(query) == "" {
		return listRecentSessions(store, limit)
	}

	// Build role filter
	var roles []string
	if roleFilter != "" {
		roles = []string{roleFilter}
	}

	results, err := store.Search(session.SearchOptions{
		Query:       query,
		RoleFilter:  roles,
		Limit:       limit * 5, // fetch more to allow grouping
		Offset:      0,
		ExcludeSources: []string{"tool"}, // exclude third-party tool sessions
	})
	if err != nil {
		return toolError(fmt.Sprintf("search failed: %v", err))
	}

	// Group by session_id
	type sessionGroup struct {
		SessionID      string
		Source         string
		SessionStarted float64
		Snippet        string
		Rank           float64
		Messages       []session.SearchResult
	}
	groups := make(map[string]*sessionGroup)
	for i := range results {
		r := &results[i]
		if _, ok := groups[r.SessionID]; !ok {
			groups[r.SessionID] = &sessionGroup{
				SessionID:      r.SessionID,
				Source:         r.Source,
				SessionStarted: r.SessionStarted,
				Rank:           float64(len(groups)), // preserve order
			}
		}
		groups[r.SessionID].Snippet = r.Snippet
		groups[r.SessionID].Messages = append(groups[r.SessionID].Messages, *r)
	}

	// Sort by relevance (session with most matches first)
	type sortedGroup struct {
		id       string
		g        *sessionGroup
		msgCount int
	}
	var sorted []sortedGroup
	for id, g := range groups {
		sorted = append(sorted, sortedGroup{id: id, g: g, msgCount: len(g.Messages)})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].msgCount > sorted[j].msgCount
	})

	// Take top N sessions
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}

	output := make([]map[string]any, 0, len(sorted))
	for _, sg := range sorted {
		g := sg.g
		started := time.Unix(int64(g.SessionStarted), 0).Format("January 02, 2006 at 03:04 PM")
		output = append(output, map[string]any{
			"session_id":   g.SessionID,
			"source":       g.Source,
			"started_at":   started,
			"match_count":  len(g.Messages),
			"snippet":      g.Snippet,
			"relevance":    "high", // placeholder; FTS5 rank not exposed directly
		})
	}

	return toolResultData(map[string]any{
		"query":      query,
		"mode":      "search",
		"sessions":   output,
		"total_hits": len(results),
		"count":      len(output),
	})
}

func listRecentSessions(store *session.Store, limit int) string {
	sessions, err := store.ListSessionsRich("", []string{"tool"}, limit, 0)
	if err != nil {
		logger := slog.Default()
		logger.Warn("list recent sessions failed", "error", err)
		return toolError(fmt.Sprintf("failed to list sessions: %v", err))
	}

	results := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		started := ""
		if s.StartedAt > 0 {
			started = time.Unix(int64(s.StartedAt), 0).Format("January 02, 2006 at 03:04 PM")
		}
		results = append(results, map[string]any{
			"session_id":   s.ID,
			"title":        s.Title,
			"source":       s.Source,
			"started_at":   started,
			"message_count": s.MessageCount,
			"preview":      s.Preview,
		})
	}

	return toolResultData(map[string]any{
		"mode":   "recent",
		"sessions": results,
		"count":  len(results),
	})
}

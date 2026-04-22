package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// SearchResult represents a single FTS5 search result.
type SearchResult struct {
	ID              int64    `json:"id"`
	SessionID       string   `json:"session_id"`
	Role            string   `json:"role"`
	Snippet         string   `json:"snippet"`
	Timestamp       float64  `json:"timestamp"`
	ToolName        *string  `json:"tool_name,omitempty"`
	Source          string   `json:"source"`
	Model           *string  `json:"model,omitempty"`
	SessionStarted  float64  `json:"session_started"`
	Context         []ContextMessage `json:"context,omitempty"`
}

// ContextMessage is a message surrounding a search match.
type ContextMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SearchOptions controls FTS5 search behaviour.
type SearchOptions struct {
	Query         string
	SourceFilter  []string // include only these sources; nil = all
	ExcludeSources []string // exclude these sources; nil = none
	RoleFilter    []string // include only these roles; nil = all
	Limit         int
	Offset        int
}

// sanitizeFTS5Query sanitizes user input for safe use in FTS5 MATCH queries.
// Preserves properly paired double-quoted phrases; strips other FTS5-special
// characters; wraps hyphenated/dotted terms in quotes to preserve phrase semantics.
func sanitizeFTS5Query(query string) string {
	// Step 1: Extract balanced double-quoted phrases and protect them.
	quotedParts := make([]string, 0, 4)
	sanitized := regexp.MustCompile(`"[^"]*"`).ReplaceAllStringFunc(query, func(m string) string {
		quotedParts = append(quotedParts, m)
		return fmt.Sprintf("\x00Q%d\x00", len(quotedParts)-1)
	})

	// Step 2: Strip FTS5-special characters.
	sanitized = regexp.MustCompile(`[+{}()"^]`).ReplaceAllString(sanitized, " ")

	// Step 3: Collapse repeated * and remove leading *.
	sanitized = regexp.MustCompile(`\*+`).ReplaceAllString(sanitized, "*")
	sanitized = regexp.MustCompile(`(^|\s)\*`).ReplaceAllString(sanitized, "")

	// Step 4: Remove dangling boolean operators at start/end.
	sanitized = strings.TrimSpace(sanitized)
	sanitized = regexp.MustCompile(`(?i)^(AND|OR|NOT)\b\s*`).ReplaceAllString(sanitized, "")
	sanitized = regexp.MustCompile(`(?i)\s+(AND|OR|NOT)\s*$`).ReplaceAllString(sanitized, "")
	sanitized = strings.TrimSpace(sanitized)

	// Step 5: Wrap unquoted dotted/hyphenated terms in double quotes so FTS5
	// treats them as exact phrases instead of splitting on the punctuation.
	sanitized = regexp.MustCompile(`\b(\w+(?:[.\-]\w+)+)\b`).ReplaceAllString(sanitized, `"$1"`)

	// Step 6: Restore preserved quoted phrases.
	for i, q := range quotedParts {
		sanitized = strings.Replace(sanitized, fmt.Sprintf("\x00Q%d\x00", i), q, 1)
	}

	return strings.TrimSpace(sanitized)
}

// containsCJK reports whether text contains CJK (Chinese, Japanese, Korean) characters.
func containsCJK(text string) bool {
	for _, r := range text {
		if isCJK(r) {
			return true
		}
	}
	return false
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) ||    // CJK Extension A
		(r >= 0x20000 && r <= 0x2A6DF) ||  // CJK Extension B
		(r >= 0x3000 && r <= 0x303F) ||    // CJK Symbols
		(r >= 0x3040 && r <= 0x309F) ||    // Hiragana
		(r >= 0x30A0 && r <= 0x30FF) ||    // Katakana
		(r >= 0xAC00 && r <= 0xD7AF)       // Hangul Syllables
}

// Search performs a full-text search across session messages using FTS5.
// Returns matching messages with session metadata, content snippets, and
// surrounding context (1 message before and after each match).
//
// Supports FTS5 query syntax:
//   - Keywords: "docker deployment"
//   - Phrases: '"exact phrase"'
//   - Boolean: "docker OR kubernetes", "python NOT java"
//   - Prefix: "deploy*"
func (s *Store) Search(opts SearchOptions) ([]SearchResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	query := sanitizeFTS5Query(opts.Query)
	if query == "" {
		return nil, nil
	}

	// Build WHERE clauses dynamically.
	whereClauses := []string{"messages_fts MATCH ?"}
	params := []any{query}

	if len(opts.SourceFilter) > 0 {
		placeholders := make([]string, len(opts.SourceFilter))
		for i, src := range opts.SourceFilter {
			placeholders[i] = "?"
			params = append(params, src)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("s.source IN (%s)", strings.Join(placeholders, ",")))
	}
	if len(opts.ExcludeSources) > 0 {
		placeholders := make([]string, len(opts.ExcludeSources))
		for i, src := range opts.ExcludeSources {
			placeholders[i] = "?"
			params = append(params, src)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("s.source NOT IN (%s)", strings.Join(placeholders, ",")))
	}
	if len(opts.RoleFilter) > 0 {
		placeholders := make([]string, len(opts.RoleFilter))
		for i, role := range opts.RoleFilter {
			placeholders[i] = "?"
			params = append(params, role)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("m.role IN (%s)", strings.Join(placeholders, ",")))
	}

	whereSQL := strings.Join(whereClauses, " AND ")
	params = append(params, opts.Limit, opts.Offset)

	sqlQuery := fmt.Sprintf(`
		SELECT
			m.id,
			m.session_id,
			m.role,
			snippet(messages_fts, 0, '>>>', '<<<', '...', 40) AS snippet,
			m.content,
			m.timestamp,
			m.tool_name,
			s.source,
			s.model,
			s.started_at AS session_started
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.rowid
		JOIN sessions s ON s.id = m.session_id
		WHERE %s
		ORDER BY rank
		LIMIT ? OFFSET ?
	`, whereSQL)

	var matches []SearchResult
	s.lock.Lock()
	err := func() error {
		rows, err := s.conn.Query(sqlQuery, params...)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var r SearchResult
			var content, toolName, model sql.NullString
			if err := rows.Scan(&r.ID, &r.SessionID, &r.Role, &r.Snippet,
				&content, &r.Timestamp, &toolName,
				&r.Source, &model, &r.SessionStarted); err != nil {
				return err
			}
			if toolName.Valid {
				r.ToolName = &toolName.String
			}
			if model.Valid {
				r.Model = &model.String
			}
			matches = append(matches, r)
		}
		return rows.Err()
	}()

	// FTS5 query syntax error — fall back to LIKE for CJK queries.
	if err != nil {
		if !containsCJK(query) {
			return nil, err
		}
		matches, err = s.searchLikeFallback(opts)
		if err != nil {
			return nil, err
		}
	}
	s.lock.Unlock()

	// Add surrounding context (1 message before + after each match).
	for i := range matches {
		ctx, _ := s.getMessageContext(matches[i].ID)
		matches[i].Context = ctx
	}

	// Remove full content from result (snippet is enough).
	for i := range matches {
		_ = matches[i] // content already excluded via SELECT snippet not content
	}

	return matches, nil
}

// searchLikeFallback performs a LIKE-based search for CJK queries where
// FTS5's individual-character tokenization is unhelpful.
func (s *Store) searchLikeFallback(opts SearchOptions) ([]SearchResult, error) {
	rawQuery := strings.Trim(opts.Query, `" `)
	if rawQuery == "" {
		return nil, nil
	}

	whereClauses := []string{"m.content LIKE ?"}
	params := []any{fmt.Sprintf("%%%s%%", rawQuery)}

	if len(opts.SourceFilter) > 0 {
		placeholders := make([]string, len(opts.SourceFilter))
		for i, src := range opts.SourceFilter {
			placeholders[i] = "?"
			params = append(params, src)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("s.source IN (%s)", strings.Join(placeholders, ",")))
	}
	if len(opts.ExcludeSources) > 0 {
		placeholders := make([]string, len(opts.ExcludeSources))
		for i, src := range opts.ExcludeSources {
			placeholders[i] = "?"
			params = append(params, src)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("s.source NOT IN (%s)", strings.Join(placeholders, ",")))
	}
	if len(opts.RoleFilter) > 0 {
		placeholders := make([]string, len(opts.RoleFilter))
		for i, role := range opts.RoleFilter {
			placeholders[i] = "?"
			params = append(params, role)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("m.role IN (%s)", strings.Join(placeholders, ",")))
	}

	whereSQL := strings.Join(whereClauses, " AND ")
	params = append(params, opts.Limit, opts.Offset)

	// instr() position for snippet extraction.
	params = append([]any{rawQuery}, params...)

	sqlQuery := fmt.Sprintf(`
		SELECT m.id, m.session_id, m.role,
		       substr(m.content, max(1, instr(m.content, ?) - 40), 120) AS snippet,
		       m.content, m.timestamp, m.tool_name,
		       s.source, s.model, s.started_at AS session_started
		FROM messages m
		JOIN sessions s ON s.id = m.session_id
		WHERE %s
		ORDER BY m.timestamp DESC
		LIMIT ? OFFSET ?
	`, whereSQL)

	var results []SearchResult
	rows, err := s.conn.Query(sqlQuery, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var r SearchResult
		var content, toolName, model sql.NullString
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Role, &r.Snippet,
			&content, &r.Timestamp, &toolName,
			&r.Source, &model, &r.SessionStarted); err != nil {
			return nil, err
		}
		if toolName.Valid {
			r.ToolName = &toolName.String
		}
		if model.Valid {
			r.Model = &model.String
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// getMessageContext returns the 1 message before and 1 after the given message ID.
func (s *Store) getMessageContext(msgID int64) ([]ContextMessage, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	query := `
		WITH target AS (
			SELECT session_id, timestamp, id
			FROM messages
			WHERE id = ?
		)
		SELECT role, content FROM (
			SELECT m.id, m.timestamp, m.role, m.content
			FROM messages m
			JOIN target t ON t.session_id = m.session_id
			WHERE (m.timestamp < t.timestamp)
			   OR (m.timestamp = t.timestamp AND m.id < t.id)
			ORDER BY m.timestamp DESC, m.id DESC
			LIMIT 1
		)
		UNION ALL
		SELECT role, content FROM messages WHERE id = ?
		UNION ALL
		SELECT role, content FROM (
			SELECT m.id, m.timestamp, m.role, m.content
			FROM messages m
			JOIN target t ON t.session_id = m.session_id
			WHERE (m.timestamp > t.timestamp)
			   OR (m.timestamp = t.timestamp AND m.id > t.id)
			ORDER BY m.timestamp ASC, m.id ASC
			LIMIT 1
		)`

	rows, err := s.conn.Query(query, msgID, msgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ctx []ContextMessage
	for rows.Next() {
		var role string
		var content sql.NullString
		if err := rows.Scan(&role, &content); err != nil {
			return nil, err
		}
		c := content.String
		if len(c) > 200 {
			c = c[:200]
		}
		ctx = append(ctx, ContextMessage{Role: role, Content: c})
	}
	return ctx, rows.Err()
}

// CountCJK returns true if query contains CJK characters.
func CountCJK(query string) bool {
	return containsCJK(query)
}

// SanitizeFTS5Query exposes the sanitizer for use in tests.
func SanitizeFTS5Query(query string) string {
	return sanitizeFTS5Query(query)
}

// =============================================================================
// Title uniqueness helpers
// =============================================================================

const maxTitleLength = 100

var (
	// controlChars matches ASCII control characters (0x00-0x1F and 0x7F).
	controlChars = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)
	// unicodeControl matches problematic Unicode control characters.
	unicodeControl = regexp.MustCompile(`[\u200b-\u200f\u2028-\u202e\u2060-\u2069\ufeff\ufffc\ufff9-\ufffb]`)
	// whitespaceCollpase collapses runs of whitespace to a single space.
	whitespaceCollpase = regexp.MustCompile(`\s+`)
)

// sanitizeTitle validates and sanitizes a session title:
// - Strips leading/trailing whitespace
// - Removes ASCII and Unicode control characters
// - Collapses internal whitespace runs to single spaces
// - Normalizes empty strings to ""
// - Enforces maxTitleLength (100 chars)
//
// Returns the cleaned title or "".
func sanitizeTitle(title string) string {
	if title == "" {
		return ""
	}
	// Remove ASCII control chars (keep \t \n \r)
	cleaned := controlChars.ReplaceAllString(title, "")
	// Remove problematic Unicode control chars
	cleaned = unicodeControl.ReplaceAllString(cleaned, "")
	// Collapse whitespace runs
	cleaned = whitespaceCollpase.ReplaceAllString(cleaned, " ")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return ""
	}
	// Truncate to max length
	if len(cleaned) > maxTitleLength {
		cleaned = cleaned[:maxTitleLength]
	}
	// Remove any residual non-printable characters.
	cleaned = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, cleaned)
	return cleaned
}

// =============================================================================
// Compression tip helper
// =============================================================================

// GetCompressionTip walks the compression-continuation chain forward and returns
// the tip session ID, or the input sessionID if it has no compression continuation.
func (s *Store) GetCompressionTip(sessionID string) (string, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	current := sessionID
	for i := 0; i < 100; i++ {
		var endedAt sql.NullFloat64
		var endReason sql.NullString
		err := s.conn.QueryRow(
			`SELECT ended_at, end_reason FROM sessions WHERE id = ?`,
			current).Scan(&endedAt, &endReason)
		if err != nil {
			return current, nil
		}
		if !endedAt.Valid || endReason.String != "compression" {
			return current, nil
		}
		var childID sql.NullString
		err = s.conn.QueryRow(`
			SELECT id FROM sessions
			WHERE parent_session_id = ?
			  AND started_at >= (
			      SELECT ended_at FROM sessions WHERE id = ? AND end_reason = 'compression'
			  )
			ORDER BY started_at DESC LIMIT 1`,
			current, current).Scan(&childID)
		if !childID.Valid || err == sql.ErrNoRows {
			return current, nil
		}
		if err != nil {
			return current, err
		}
		current = childID.String
	}
	return current, nil
}

// =============================================================================
// ListSessionsRich returns sessions with preview and last-active timestamps.
// =============================================================================

// SessionRich is an enriched session with preview and last_active fields.
type SessionRich struct {
	*Session
	Preview    string  `json:"preview"`
	LastActive float64 `json:"last_active"`
}

// ListSessionsRich returns enriched sessions with preview (first 60 chars of
// first user message) and last_active (timestamp of last message).
func (s *Store) ListSessionsRich(source string, excludeSources []string, limit, offset int) ([]*SessionRich, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	whereClauses := []string{}
	params := []any{}

	if !hasValue(source) {
		whereClauses = append(whereClauses, "s.parent_session_id IS NULL")
	} else {
		whereClauses = append(whereClauses, "s.source = ?")
		params = append(params, source)
	}
	if len(excludeSources) > 0 {
		placeholders := make([]string, len(excludeSources))
		for i, src := range excludeSources {
			placeholders[i] = "?"
			params = append(params, src)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("s.source NOT IN (%s)", strings.Join(placeholders, ",")))
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT s.*,
			COALESCE(
				(SELECT SUBSTR(REPLACE(REPLACE(m.content, X'0A', ' '), X'0D', ' '), 1, 63)
				 FROM messages m
				 WHERE m.session_id = s.id AND m.role = 'user' AND m.content IS NOT NULL
				 ORDER BY m.timestamp, m.id LIMIT 1),
				''
			) AS _preview_raw,
			COALESCE(
				(SELECT MAX(m2.timestamp) FROM messages m2 WHERE m2.session_id = s.id),
				s.started_at
			) AS last_active
		FROM sessions s
		%s
		ORDER BY s.started_at DESC
		LIMIT ? OFFSET ?
	`, whereSQL)

	params = append(params, limit, offset)

	rows, err := s.conn.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*SessionRich
	for rows.Next() {
		sr := &SessionRich{Session: &Session{}}
		var previewRaw sql.NullString
		var userID, model, modelConfig, systemPrompt, parentSessionID, endReason sql.NullString
		var endedAt, estimatedCostUSD, actualCostUSD sql.NullFloat64
		var cacheReadTokens, cacheWriteTokens, reasoningTokens sql.NullInt64
		var billingProvider, billingBaseURL, billingMode, costStatus, costSource, pricingVersion, title sql.NullString

		err := rows.Scan(
			&sr.Session.ID, &sr.Session.Source, &userID, &model, &modelConfig, &systemPrompt, &parentSessionID,
			&sr.Session.StartedAt, &endedAt, &endReason,
			&sr.Session.MessageCount, &sr.Session.ToolCallCount,
			&sr.Session.InputTokens, &sr.Session.OutputTokens,
			&cacheReadTokens, &cacheWriteTokens, &reasoningTokens,
			&billingProvider, &billingBaseURL, &billingMode,
			&estimatedCostUSD, &actualCostUSD,
			&costStatus, &costSource, &pricingVersion, &title,
			&previewRaw, &sr.LastActive)
		if err != nil {
			return nil, err
		}
		if userID.Valid {
			sr.Session.UserID = &userID.String
		}
		if model.Valid {
			sr.Session.Model = &model.String
		}
		if modelConfig.Valid {
			sr.Session.ModelConfig = &modelConfig.String
		}
		if systemPrompt.Valid {
			sr.Session.SystemPrompt = &systemPrompt.String
		}
		if parentSessionID.Valid {
			sr.Session.ParentSessionID = &parentSessionID.String
		}
		if endedAt.Valid {
			sr.Session.EndedAt = &endedAt.Float64
		}
		if endReason.Valid {
			sr.Session.EndReason = &endReason.String
		}
		if cacheReadTokens.Valid {
			sr.Session.CacheReadTokens = int(cacheReadTokens.Int64)
		}
		if cacheWriteTokens.Valid {
			sr.Session.CacheWriteTokens = int(cacheWriteTokens.Int64)
		}
		if reasoningTokens.Valid {
			sr.Session.ReasoningTokens = int(reasoningTokens.Int64)
		}
		if billingProvider.Valid {
			sr.Session.BillingProvider = &billingProvider.String
		}
		if billingBaseURL.Valid {
			sr.Session.BillingBaseURL = &billingBaseURL.String
		}
		if billingMode.Valid {
			sr.Session.BillingMode = &billingMode.String
		}
		if estimatedCostUSD.Valid {
			sr.Session.EstimatedCostUSD = &estimatedCostUSD.Float64
		}
		if actualCostUSD.Valid {
			sr.Session.ActualCostUSD = &actualCostUSD.Float64
		}
		if costStatus.Valid {
			sr.Session.CostStatus = &costStatus.String
		}
		if costSource.Valid {
			sr.Session.CostSource = &costSource.String
		}
		if pricingVersion.Valid {
			sr.Session.PricingVersion = &pricingVersion.String
		}
		if title.Valid {
			sr.Session.Title = &title.String
		}
		raw := previewRaw.String
		if len(raw) > 60 {
			sr.Preview = raw[:60] + "..."
		} else {
			sr.Preview = raw
		}
		results = append(results, sr)
	}
	return results, rows.Err()
}

func hasValue(s string) bool { return s != "" }

// MarshalJSON implements json.Marshaler for Session, excluding zero-value JSON fields for cleanliness.
func (s *Session) MarshalJSON() ([]byte, error) {
	type Alias Session
	aux := struct {
		*Alias
		ModelConfigParsed map[string]any `json:"model_config_parsed,omitempty"`
	}{
		Alias: (*Alias)(s),
	}
	if s.ModelConfig != nil {
		var m map[string]any
		if err := json.Unmarshal([]byte(*s.ModelConfig), &m); err == nil {
			aux.ModelConfigParsed = m
		}
	}
	return json.Marshal(&aux)
}

package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 6

// Store is a thread-safe SQLite-backed session store with FTS5 full-text search.
type Store struct {
	dbPath string
	conn   *sql.DB

	lock         sync.Mutex
	writeCount   int
	writeRetries int
}

var (
	_WRITE_MAX_RETRIES    = 15
	_WRITE_RETRY_MIN_SEC  = 0.020
	_WRITE_RETRY_MAX_SEC  = 0.150
	_CHECKPOINT_EVERY_N   = 50
)

// NewStore returns a new Store, creating the sessions directory and DB as needed.
// DB path: ~/.hermes/sessions/state.db
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("session store: cannot find home dir: %w", err)
	}
	sessionsDir := filepath.Join(home, ".hermes", "sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		return nil, fmt.Errorf("session store: cannot create sessions dir: %w", err)
	}
	return NewStoreAt(filepath.Join(sessionsDir, "state.db"))
}

// NewStoreAt opens a store at the given dbPath, creating the DB and schema if needed.
func NewStoreAt(dbPath string) (*Store, error) {
	conn, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=1000&_foreign_keys=1")
	if err != nil {
		return nil, fmt.Errorf("session store: open failed: %w", err)
	}
	conn.SetMaxOpenConns(1) // SQLite is single-writer; serialize access via mutex

	s := &Store{dbPath: dbPath, conn: conn}
	if err := s.initSchema(); err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database connection after a passive WAL checkpoint.
func (s *Store) Close() error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.conn != nil {
		// Best-effort checkpoint
		s.conn.Exec("PRAGMA wal_checkpoint(PASSIVE)") //nolint:errcheck
		return s.conn.Close()
	}
	return nil
}

// =============================================================================
// Schema
// =============================================================================

func (s *Store) initSchema() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	schema := `
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    user_id TEXT,
    model TEXT,
    model_config TEXT,
    system_prompt TEXT,
    parent_session_id TEXT,
    started_at REAL NOT NULL,
    ended_at REAL,
    end_reason TEXT,
    message_count INTEGER DEFAULT 0,
    tool_call_count INTEGER DEFAULT 0,
    input_tokens INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,
    cache_read_tokens INTEGER DEFAULT 0,
    cache_write_tokens INTEGER DEFAULT 0,
    reasoning_tokens INTEGER DEFAULT 0,
    billing_provider TEXT,
    billing_base_url TEXT,
    billing_mode TEXT,
    estimated_cost_usd REAL,
    actual_cost_usd REAL,
    cost_status TEXT,
    cost_source TEXT,
    pricing_version TEXT,
    title TEXT,
    FOREIGN KEY (parent_session_id) REFERENCES sessions(id)
);

CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    role TEXT NOT NULL,
    content TEXT,
    tool_call_id TEXT,
    tool_calls TEXT,
    tool_name TEXT,
    timestamp REAL NOT NULL,
    token_count INTEGER,
    finish_reason TEXT,
    reasoning TEXT,
    reasoning_details TEXT,
    codex_reasoning_items TEXT
);

CREATE INDEX IF NOT EXISTS idx_sessions_source ON sessions(source);
CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id);
CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, timestamp);
`
	if _, err := s.conn.Exec(schema); err != nil {
		return fmt.Errorf("session store: schema exec failed: %w", err)
	}

	// Run migrations
	if err := s.runMigrations(); err != nil {
		return fmt.Errorf("session store: migration failed: %w", err)
	}

	// FTS5 setup
	if _, err := s.conn.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
		content,
		content=messages,
		content_rowid=id
	)`); err != nil {
		return fmt.Errorf("session store: FTS5 setup failed: %w", err)
	}

	// FTS triggers
	ftsTriggers := `
CREATE TRIGGER IF NOT EXISTS messages_fts_insert AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_delete AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, content) VALUES('delete', old.id, old.content);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_update AFTER UPDATE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, content) VALUES('delete', old.id, old.content);
    INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;
`
	if _, err := s.conn.Exec(ftsTriggers); err != nil {
		return fmt.Errorf("session store: FTS triggers failed: %w", err)
	}

	return nil
}

func (s *Store) runMigrations() error {
	var version int
	row := s.conn.QueryRow("SELECT version FROM schema_version LIMIT 1")
	if err := row.Scan(&version); err == sql.ErrNoRows {
		if _, err := s.conn.Exec("INSERT INTO schema_version (version) VALUES (?)", schemaVersion); err != nil {
			return err
		}
		version = 0
	} else if err != nil {
		return err
	}

	migrations := []struct {
		to   int
		ddl  string
		add  []string // ALTER TABLE sessions ADD COLUMN ...
	}{
		{
			to: 2,
			ddl: `ALTER TABLE messages ADD COLUMN finish_reason TEXT`,
		},
		{
			to: 3,
			ddl: `ALTER TABLE sessions ADD COLUMN title TEXT`,
		},
		{
			to: 4,
			ddl: `CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_title_unique ON sessions(title) WHERE title IS NOT NULL`,
		},
		{
			to: 5,
			add: []string{
				`ALTER TABLE sessions ADD COLUMN cache_read_tokens INTEGER DEFAULT 0`,
				`ALTER TABLE sessions ADD COLUMN cache_write_tokens INTEGER DEFAULT 0`,
				`ALTER TABLE sessions ADD COLUMN reasoning_tokens INTEGER DEFAULT 0`,
				`ALTER TABLE sessions ADD COLUMN billing_provider TEXT`,
				`ALTER TABLE sessions ADD COLUMN billing_base_url TEXT`,
				`ALTER TABLE sessions ADD COLUMN billing_mode TEXT`,
				`ALTER TABLE sessions ADD COLUMN estimated_cost_usd REAL`,
				`ALTER TABLE sessions ADD COLUMN actual_cost_usd REAL`,
				`ALTER TABLE sessions ADD COLUMN cost_status TEXT`,
				`ALTER TABLE sessions ADD COLUMN cost_source TEXT`,
				`ALTER TABLE sessions ADD COLUMN pricing_version TEXT`,
			},
		},
		{
			to: 6,
			add: []string{
				`ALTER TABLE messages ADD COLUMN reasoning TEXT`,
				`ALTER TABLE messages ADD COLUMN reasoning_details TEXT`,
				`ALTER TABLE messages ADD COLUMN codex_reasoning_items TEXT`,
			},
		},
	}

	for _, m := range migrations {
		if version < m.to {
			if m.ddl != "" {
				s.conn.Exec(m.ddl) //nolint:errcheck
			}
			for _, add := range m.add {
				s.conn.Exec(add) //nolint:errcheck
			}
			s.conn.Exec("UPDATE schema_version SET version = ?", m.to) //nolint:errcheck
		}
	}

	// Ensure title index exists (safe to re-run)
	s.conn.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_title_unique ON sessions(title) WHERE title IS NOT NULL`) //nolint:errcheck

	return nil
}

// =============================================================================
// Write helper
// =============================================================================

func (s *Store) executeWrite(fn func(*sql.Tx) error) error {
	var lastErr error
	for attempt := 0; attempt < _WRITE_MAX_RETRIES; attempt++ {
		s.lock.Lock()
		tx, err := s.conn.Begin()
		if err != nil {
			s.lock.Unlock()
			return fmt.Errorf("session store: begin failed: %w", err)
		}
		if err := fn(tx); err != nil {
			tx.Rollback() //nolint:errcheck
			s.lock.Unlock()
			// Retry on lock contention
			if isLockedErr(err) && attempt < _WRITE_MAX_RETRIES-1 {
				lastErr = err
				time.Sleep(randomJitter())
				continue
			}
			return err
		}
		if err := tx.Commit(); err != nil {
			tx.Rollback() //nolint:errcheck
			s.lock.Unlock()
			if isLockedErr(err) && attempt < _WRITE_MAX_RETRIES-1 {
				lastErr = err
				time.Sleep(randomJitter())
				continue
			}
			return fmt.Errorf("session store: commit failed: %w", err)
		}
		s.lock.Unlock()

		s.writeCount++
		if s.writeCount%_CHECKPOINT_EVERY_N == 0 {
			s.tryCheckpoint()
		}
		return nil
	}
	return lastErr
}

func isLockedErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsFold(msg, "locked") || containsFold(msg, "busy")
}

func containsFold(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || containsFoldInner(s, substr))
}

func containsFoldInner(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if equalFold(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca == cb {
			continue
		}
		if ca >= 'A' && ca <= 'Z' && ca+'a'-'A' == cb {
			continue
		}
		if cb >= 'A' && cb <= 'Z' && cb+'a'-'A' == ca {
			continue
		}
		return false
	}
	return true
}

func randomJitter() time.Duration {
	delta := _WRITE_RETRY_MAX_SEC - _WRITE_RETRY_MIN_SEC
	return time.Duration((_WRITE_RETRY_MIN_SEC+delta*rand.Float64())*1e9) * time.Nanosecond
}

func (s *Store) tryCheckpoint() {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.conn.Exec("PRAGMA wal_checkpoint(PASSIVE)") //nolint:errcheck
}

// =============================================================================
// Session lifecycle
// =============================================================================

// CreateSession creates a new session. Returns the session ID.
func (s *Store) CreateSession(id, source, model string, modelConfig map[string]any, systemPrompt, userID, parentSessionID string) (string, error) {
	var cfgJSON *string
	if modelConfig != nil {
		b, err := json.Marshal(modelConfig)
		if err != nil {
			return "", fmt.Errorf("session store: model config marshal: %w", err)
		}
		v := string(b)
		cfgJSON = &v
	}

	startedAt := float64(time.Now().Unix())

	err := s.executeWrite(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO sessions
			(id, source, user_id, model, model_config, system_prompt, parent_session_id, started_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, source, userID, model, cfgJSON, strPtr(systemPrompt), strPtr(parentSessionID), startedAt)
		return err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// EndSession marks a session as ended. No-op if already ended.
func (s *Store) EndSession(sessionID, endReason string) error {
	return s.executeWrite(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE sessions SET ended_at = ?, end_reason = ? WHERE id = ? AND ended_at IS NULL`,
			float64(time.Now().Unix()), endReason, sessionID)
		return err
	})
}

// ReopenSession clears ended_at/end_reason so a session can be resumed.
func (s *Store) ReopenSession(sessionID string) error {
	return s.executeWrite(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE sessions SET ended_at = NULL, end_reason = NULL WHERE id = ?`,
			sessionID)
		return err
	})
}

// UpdateSystemPrompt stores the full assembled system prompt snapshot.
func (s *Store) UpdateSystemPrompt(sessionID, systemPrompt string) error {
	return s.executeWrite(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE sessions SET system_prompt = ? WHERE id = ?`, systemPrompt, sessionID)
		return err
	})
}

// UpdateTokenCounts updates token counters. If absolute is false, values are
// incremented; if true, values are set directly.
func (s *Store) UpdateTokenCounts(sessionID string, inputTokens, outputTokens, cacheRead, cacheWrite, reasoningTokens int,
	absolute bool, model *string, billingProvider, billingBaseURL, billingMode *string,
	estimatedCostUSD, actualCostUSD *float64,
	costStatus, costSource, pricingVersion *string,
) error {
	return s.executeWrite(func(tx *sql.Tx) error {
		if absolute {
			_, err := tx.Exec(`
				UPDATE sessions SET
					input_tokens = ?,
					output_tokens = ?,
					cache_read_tokens = ?,
					cache_write_tokens = ?,
					reasoning_tokens = ?,
					estimated_cost_usd = COALESCE(?, 0),
					actual_cost_usd = COALESCE(?, actual_cost_usd),
					cost_status = COALESCE(?, cost_status),
					cost_source = COALESCE(?, cost_source),
					pricing_version = COALESCE(?, pricing_version),
					billing_provider = COALESCE(?, billing_provider),
					billing_base_url = COALESCE(?, billing_base_url),
					billing_mode = COALESCE(?, billing_mode),
					model = COALESCE(model, ?)
				WHERE id = ?`,
				inputTokens, outputTokens, cacheRead, cacheWrite, reasoningTokens,
				estimatedCostUSD, actualCostUSD,
				costStatus, costSource, pricingVersion,
				billingProvider, billingBaseURL, billingMode,
				model, sessionID)
			return err
		}
		_, err := tx.Exec(`
			UPDATE sessions SET
				input_tokens = input_tokens + ?,
				output_tokens = output_tokens + ?,
				cache_read_tokens = cache_read_tokens + ?,
				cache_write_tokens = cache_write_tokens + ?,
				reasoning_tokens = reasoning_tokens + ?,
				estimated_cost_usd = COALESCE(estimated_cost_usd, 0) + COALESCE(?, 0),
				actual_cost_usd = COALESCE(actual_cost_usd, 0) + COALESCE(?, 0),
				cost_status = COALESCE(?, cost_status),
				cost_source = COALESCE(?, cost_source),
				pricing_version = COALESCE(?, pricing_version),
				billing_provider = COALESCE(?, billing_provider),
				billing_base_url = COALESCE(?, billing_base_url),
				billing_mode = COALESCE(?, billing_mode),
				model = COALESCE(model, ?)
			WHERE id = ?`,
			inputTokens, outputTokens, cacheRead, cacheWrite, reasoningTokens,
			estimatedCostUSD, actualCostUSD,
			costStatus, costSource, pricingVersion,
			billingProvider, billingBaseURL, billingMode,
			model, sessionID)
		return err
	})
}

// EnsureSession creates a session row if it doesn't exist (INSERT OR IGNORE).
func (s *Store) EnsureSession(sessionID, source, model string) error {
	return s.executeWrite(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT OR IGNORE INTO sessions (id, source, model, started_at) VALUES (?, ?, ?, ?)`,
			sessionID, source, model, float64(time.Now().Unix()))
		return err
	})
}

// GetSession returns a session by ID, or nil if not found.
func (s *Store) GetSession(sessionID string) (*Session, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	row := s.conn.QueryRow(`SELECT * FROM sessions WHERE id = ?`, sessionID)
	return scanSession(row)
}

// ListSessions returns sessions ordered by started_at DESC.
func (s *Store) ListSessions(source string, limit, offset int) ([]*Session, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	var rows *sql.Rows
	var err error
	if source != "" {
		rows, err = s.conn.Query(
			`SELECT * FROM sessions WHERE source = ? ORDER BY started_at DESC LIMIT ? OFFSET ?`,
			source, limit, offset)
	} else {
		rows, err = s.conn.Query(
			`SELECT * FROM sessions ORDER BY started_at DESC LIMIT ? OFFSET ?`,
			limit, offset)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSessions(rows)
}

// SessionCount returns the total number of sessions, optionally filtered by source.
func (s *Store) SessionCount(source string) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	var row *sql.Row
	if source != "" {
		row = s.conn.QueryRow(`SELECT COUNT(*) FROM sessions WHERE source = ?`, source)
	} else {
		row = s.conn.QueryRow(`SELECT COUNT(*) FROM sessions`)
	}
	var count int
	return count, row.Scan(&count)
}

// SetSessionTitle sets or clears a session's title. Returns (found, error).
func (s *Store) SetSessionTitle(sessionID string, title *string) (bool, error) {
	if title != nil {
		title = strPtr(sanitizeTitle(*title))
	}
	err := s.executeWrite(func(tx *sql.Tx) error {
		if title != nil && *title != "" {
			// Check uniqueness (allow the same session to keep its own title)
			var conflictID string
			row := tx.QueryRow(`SELECT id FROM sessions WHERE title = ? AND id != ?`, *title, sessionID)
			if err := row.Scan(&conflictID); err == nil {
				return fmt.Errorf("title %q is already in use", *title)
			} else if err != sql.ErrNoRows {
				return err
			}
		}
		res, err := tx.Exec(`UPDATE sessions SET title = ? WHERE id = ?`, ptrToStr(title), sessionID)
		if err != nil {
			return err
		}
		_, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return false, err
	}
	// Check if session exists
	s.lock.Lock()
	defer s.lock.Unlock()
	var count int
	s.conn.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, sessionID).Scan(&count) //nolint:errcheck
	return count > 0, nil
}

// ResolveSessionID resolves an exact or uniquely prefixed session ID.
func (s *Store) ResolveSessionID(prefix string) (string, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	// Try exact match first
	var exactID string
	err := s.conn.QueryRow(`SELECT id FROM sessions WHERE id = ?`, prefix).Scan(&exactID)
	if err == nil {
		return exactID, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	// Prefix search
	escaped := escapeLike(prefix)
	rows, err := s.conn.Query(
		`SELECT id FROM sessions WHERE id LIKE ? ESCAPE '\' ORDER BY started_at DESC LIMIT 2`,
		escaped+"%")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if len(ids) == 1 {
		return ids[0], nil
	}
	return "", nil
}

// =============================================================================
// Message storage
// =============================================================================

// AppendMessage appends a message to a session. Returns the message row ID.
func (s *Store) AppendMessage(msg *Message) (int64, error) {
	var toolCallsJSON *string
	if len(msg.ToolCalls) > 0 {
		v := string(msg.ToolCalls)
		toolCallsJSON = &v
	}
	var reasoningDetailsJSON *string
	if len(msg.ReasoningDetails) > 0 {
		v := string(msg.ReasoningDetails)
		reasoningDetailsJSON = &v
	}
	var codexItemsJSON *string
	if len(msg.CodexReasoningItems) > 0 {
		v := string(msg.CodexReasoningItems)
		codexItemsJSON = &v
	}

	var msgID int64
	err := s.executeWrite(func(tx *sql.Tx) error {
		result, err := tx.Exec(`
			INSERT INTO messages (session_id, role, content, tool_call_id,
				tool_calls, tool_name, timestamp, token_count, finish_reason,
				reasoning, reasoning_details, codex_reasoning_items)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			msg.SessionID, msg.Role, msg.Content, msg.ToolCallID,
			toolCallsJSON, msg.ToolName, float64(time.Now().Unix()),
			msg.TokenCount, msg.FinishReason,
			msg.Reasoning, reasoningDetailsJSON, codexItemsJSON)
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		msgID = id

		numToolCalls := 0
		if len(msg.ToolCalls) > 0 {
			var tc []any
			if json.Unmarshal(msg.ToolCalls, &tc) == nil {
				numToolCalls = len(tc)
			}
		}
		if numToolCalls > 0 {
			_, err = tx.Exec(
				`UPDATE sessions SET message_count = message_count + 1, tool_call_count = tool_call_count + ? WHERE id = ?`,
				numToolCalls, msg.SessionID)
		} else {
			_, err = tx.Exec(
				`UPDATE sessions SET message_count = message_count + 1 WHERE id = ?`,
				msg.SessionID)
		}
		return err
	})
	return msgID, err
}

// GetMessages returns all messages for a session ordered by timestamp.
func (s *Store) GetMessages(sessionID string) ([]*Message, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	rows, err := s.conn.Query(
		`SELECT * FROM messages WHERE session_id = ? ORDER BY timestamp, id`,
		sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// MessageCount returns the total message count, optionally for a specific session.
func (s *Store) MessageCount(sessionID string) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	var row *sql.Row
	if sessionID != "" {
		row = s.conn.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID)
	} else {
		row = s.conn.QueryRow(`SELECT COUNT(*) FROM messages`)
	}
	var count int
	return count, row.Scan(&count)
}

// ClearMessages deletes all messages for a session and resets counters.
func (s *Store) ClearMessages(sessionID string) error {
	return s.executeWrite(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ?`, sessionID); err != nil {
			return err
		}
		_, err := tx.Exec(
			`UPDATE sessions SET message_count = 0, tool_call_count = 0 WHERE id = ?`,
			sessionID)
		return err
	})
}

// DeleteSession deletes a session and all its messages.
func (s *Store) DeleteSession(sessionID string) (bool, error) {
	var deleted bool
	err := s.executeWrite(func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, sessionID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			deleted = false
			return nil
		}
		deleted = true
		// Orphan children
		tx.Exec(`UPDATE sessions SET parent_session_id = NULL WHERE parent_session_id = ?`, sessionID) //nolint:errcheck
		tx.Exec(`DELETE FROM messages WHERE session_id = ?`, sessionID)                                  //nolint:errcheck
		_, err := tx.Exec(`DELETE FROM sessions WHERE id = ?`, sessionID)
		return err
	})
	return deleted, err
}

// =============================================================================
// Export
// =============================================================================

// ExportSession exports a session with all its messages, or nil if not found.
func (s *Store) ExportSession(sessionID string) (*Session, []*Message, error) {
	sess, err := s.GetSession(sessionID)
	if err != nil || sess == nil {
		return nil, nil, err
	}
	msgs, err := s.GetMessages(sessionID)
	if err != nil {
		return nil, nil, err
	}
	return sess, msgs, nil
}

// =============================================================================
// Helpers
// =============================================================================

func scanSession(row *sql.Row) (*Session, error) {
	s := &Session{}
	var userID, model, modelConfig, systemPrompt, parentSessionID, endReason sql.NullString
	var endedAt, estimatedCostUSD, actualCostUSD sql.NullFloat64
	var cacheReadTokens, cacheWriteTokens, reasoningTokens sql.NullInt64
	var billingProvider, billingBaseURL, billingMode, costStatus, costSource, pricingVersion, title sql.NullString

	err := row.Scan(
		&s.ID, &s.Source, &userID, &model, &modelConfig, &systemPrompt, &parentSessionID,
		&s.StartedAt, &endedAt, &endReason,
		&s.MessageCount, &s.ToolCallCount,
		&s.InputTokens, &s.OutputTokens,
		&cacheReadTokens, &cacheWriteTokens, &reasoningTokens,
		&billingProvider, &billingBaseURL, &billingMode,
		&estimatedCostUSD, &actualCostUSD,
		&costStatus, &costSource, &pricingVersion, &title)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if userID.Valid {
		s.UserID = &userID.String
	}
	if model.Valid {
		s.Model = &model.String
	}
	if modelConfig.Valid {
		s.ModelConfig = &modelConfig.String
	}
	if systemPrompt.Valid {
		s.SystemPrompt = &systemPrompt.String
	}
	if parentSessionID.Valid {
		s.ParentSessionID = &parentSessionID.String
	}
	if endedAt.Valid {
		s.EndedAt = &endedAt.Float64
	}
	if endReason.Valid {
		s.EndReason = &endReason.String
	}
	if cacheReadTokens.Valid {
		s.CacheReadTokens = int(cacheReadTokens.Int64)
	}
	if cacheWriteTokens.Valid {
		s.CacheWriteTokens = int(cacheWriteTokens.Int64)
	}
	if reasoningTokens.Valid {
		s.ReasoningTokens = int(reasoningTokens.Int64)
	}
	if billingProvider.Valid {
		s.BillingProvider = &billingProvider.String
	}
	if billingBaseURL.Valid {
		s.BillingBaseURL = &billingBaseURL.String
	}
	if billingMode.Valid {
		s.BillingMode = &billingMode.String
	}
	if estimatedCostUSD.Valid {
		s.EstimatedCostUSD = &estimatedCostUSD.Float64
	}
	if actualCostUSD.Valid {
		s.ActualCostUSD = &actualCostUSD.Float64
	}
	if costStatus.Valid {
		s.CostStatus = &costStatus.String
	}
	if costSource.Valid {
		s.CostSource = &costSource.String
	}
	if pricingVersion.Valid {
		s.PricingVersion = &pricingVersion.String
	}
	if title.Valid {
		s.Title = &title.String
	}

	return s, nil
}

func scanSessions(rows *sql.Rows) ([]*Session, error) {
	var sessions []*Session
	for rows.Next() {
		s := &Session{}
		var userID, model, modelConfig, systemPrompt, parentSessionID, endReason sql.NullString
		var endedAt, estimatedCostUSD, actualCostUSD sql.NullFloat64
		var cacheReadTokens, cacheWriteTokens, reasoningTokens sql.NullInt64
		var billingProvider, billingBaseURL, billingMode, costStatus, costSource, pricingVersion, title sql.NullString

		err := rows.Scan(
			&s.ID, &s.Source, &userID, &model, &modelConfig, &systemPrompt, &parentSessionID,
			&s.StartedAt, &endedAt, &endReason,
			&s.MessageCount, &s.ToolCallCount,
			&s.InputTokens, &s.OutputTokens,
			&cacheReadTokens, &cacheWriteTokens, &reasoningTokens,
			&billingProvider, &billingBaseURL, &billingMode,
			&estimatedCostUSD, &actualCostUSD,
			&costStatus, &costSource, &pricingVersion, &title)
		if err != nil {
			return nil, err
		}
		if userID.Valid {
			s.UserID = &userID.String
		}
		if model.Valid {
			s.Model = &model.String
		}
		if modelConfig.Valid {
			s.ModelConfig = &modelConfig.String
		}
		if systemPrompt.Valid {
			s.SystemPrompt = &systemPrompt.String
		}
		if parentSessionID.Valid {
			s.ParentSessionID = &parentSessionID.String
		}
		if endedAt.Valid {
			s.EndedAt = &endedAt.Float64
		}
		if endReason.Valid {
			s.EndReason = &endReason.String
		}
		if cacheReadTokens.Valid {
			s.CacheReadTokens = int(cacheReadTokens.Int64)
		}
		if cacheWriteTokens.Valid {
			s.CacheWriteTokens = int(cacheWriteTokens.Int64)
		}
		if reasoningTokens.Valid {
			s.ReasoningTokens = int(reasoningTokens.Int64)
		}
		if billingProvider.Valid {
			s.BillingProvider = &billingProvider.String
		}
		if billingBaseURL.Valid {
			s.BillingBaseURL = &billingBaseURL.String
		}
		if billingMode.Valid {
			s.BillingMode = &billingMode.String
		}
		if estimatedCostUSD.Valid {
			s.EstimatedCostUSD = &estimatedCostUSD.Float64
		}
		if actualCostUSD.Valid {
			s.ActualCostUSD = &actualCostUSD.Float64
		}
		if costStatus.Valid {
			s.CostStatus = &costStatus.String
		}
		if costSource.Valid {
			s.CostSource = &costSource.String
		}
		if pricingVersion.Valid {
			s.PricingVersion = &pricingVersion.String
		}
		if title.Valid {
			s.Title = &title.String
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

func scanMessages(rows *sql.Rows) ([]*Message, error) {
	var messages []*Message
	for rows.Next() {
		m := &Message{}
		var content, toolCallID, toolCalls, toolName, finishReason, reasoning, reasoningDetails, codexItems sql.NullString
		var tokenCount sql.NullInt64

		err := rows.Scan(
			&m.ID, &m.SessionID, &m.Role, &content, &toolCallID,
			&toolCalls, &toolName, &m.Timestamp, &tokenCount,
			&finishReason, &reasoning, &reasoningDetails, &codexItems)
		if err != nil {
			return nil, err
		}
		if content.Valid {
			m.Content = &content.String
		}
		if toolCallID.Valid {
			m.ToolCallID = &toolCallID.String
		}
		if toolCalls.Valid {
			m.ToolCalls = json.RawMessage(toolCalls.String)
		}
		if toolName.Valid {
			m.ToolName = &toolName.String
		}
		if tokenCount.Valid {
			v := int(tokenCount.Int64)
			m.TokenCount = &v
		}
		if finishReason.Valid {
			m.FinishReason = &finishReason.String
		}
		if reasoning.Valid {
			m.Reasoning = &reasoning.String
		}
		if reasoningDetails.Valid {
			m.ReasoningDetails = json.RawMessage(reasoningDetails.String)
		}
		if codexItems.Valid {
			m.CodexReasoningItems = json.RawMessage(codexItems.String)
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func strPtr(v string) *string { return &v }
func ptrToStr(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// escapeLike escapes SQL LIKE wildcard characters.
func escapeLike(s string) string {
	s = regexp.MustCompile(`([\\%_])`).ReplaceAllString(s, `\$1`)
	return s
}

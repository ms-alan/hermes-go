package session

import (
	"encoding/json"
	"time"
)

// Session represents a conversation session stored in SQLite.
type Session struct {
	ID                string     `json:"id"`
	Source            string     `json:"source"`
	UserID            *string    `json:"user_id,omitempty"`
	Model             *string    `json:"model,omitempty"`
	ModelConfig       *string    `json:"model_config,omitempty"` // JSON
	SystemPrompt      *string    `json:"system_prompt,omitempty"`
	ParentSessionID   *string    `json:"parent_session_id,omitempty"`
	StartedAt         float64    `json:"started_at"`
	EndedAt           *float64   `json:"ended_at,omitempty"`
	EndReason         *string    `json:"end_reason,omitempty"`
	MessageCount      int        `json:"message_count"`
	ToolCallCount     int        `json:"tool_call_count"`
	InputTokens       int        `json:"input_tokens"`
	OutputTokens      int        `json:"output_tokens"`
	CacheReadTokens   int        `json:"cache_read_tokens"`
	CacheWriteTokens  int        `json:"cache_write_tokens"`
	ReasoningTokens   int        `json:"reasoning_tokens"`
	BillingProvider   *string    `json:"billing_provider,omitempty"`
	BillingBaseURL    *string    `json:"billing_base_url,omitempty"`
	BillingMode       *string    `json:"billing_mode,omitempty"`
	EstimatedCostUSD  *float64   `json:"estimated_cost_usd,omitempty"`
	ActualCostUSD     *float64   `json:"actual_cost_usd,omitempty"`
	CostStatus        *string    `json:"cost_status,omitempty"`
	CostSource        *string    `json:"cost_source,omitempty"`
	PricingVersion    *string    `json:"pricing_version,omitempty"`
	Title             *string    `json:"title,omitempty"`
}

// Message represents a single message within a session.
type Message struct {
	ID                  int64           `json:"id"`
	SessionID           string          `json:"session_id"`
	Role                string          `json:"role"`
	Content             *string         `json:"content,omitempty"`
	ToolCallID          *string         `json:"tool_call_id,omitempty"`
	ToolCalls           json.RawMessage `json:"tool_calls,omitempty"` // JSON array
	ToolName            *string         `json:"tool_name,omitempty"`
	Timestamp           float64         `json:"timestamp"`
	TokenCount          *int            `json:"token_count,omitempty"`
	FinishReason        *string         `json:"finish_reason,omitempty"`
	Reasoning           *string         `json:"reasoning,omitempty"`
	ReasoningDetails    json.RawMessage `json:"reasoning_details,omitempty"`
	CodexReasoningItems json.RawMessage `json:"codex_reasoning_items,omitempty"`
}

// ModelConfig parses and returns the model_config JSON as a map.
func (s *Session) GetModelConfig() map[string]any {
	if s.ModelConfig == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(*s.ModelConfig), &m); err != nil {
		return nil
	}
	return m
}

// StartedTime returns the session start time as a time.Time.
func (s *Session) StartedTime() time.Time {
	return time.Unix(int64(s.StartedAt), 0)
}

// EndedTime returns the session end time, or zero time if not ended.
func (s *Session) EndedTime() time.Time {
	if s.EndedAt == nil {
		return time.Time{}
	}
	return time.Unix(int64(*s.EndedAt), 0)
}

// IsEnded returns true if the session has ended.
func (s *Session) IsEnded() bool {
	return s.EndedAt != nil
}

// Duration returns the session duration, or elapsed time if not ended.
func (s *Session) Duration() time.Duration {
	end := s.EndedAt
	if end == nil {
		end = ptr(float64(time.Now().Unix()))
	}
	return time.Duration(int64(*end-s.StartedAt)) * time.Second
}

func ptr[T any](v T) *T {
	return &v
}

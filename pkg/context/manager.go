package context

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nousresearch/hermes-go/pkg/model"
)

// ManagerConfig holds the context window manager configuration.
type ManagerConfig struct {
	ModelContextLength int
	// ReservedTokens is the number of tokens reserved for system prompt / overhead.
	ReservedTokens int
	// CompressionConfig configures the compressor.
	CompressionConfig CompressorConfig
	// CacheTTL is the TTL for system prompt caching.
	CacheTTL time.Duration
	// SummarizerModel is the model name used for context compression summaries.
	// If empty, uses the same model as the main LLM.
	SummarizerModel string
}

// DefaultManagerConfig returns sensible defaults.
func DefaultManagerConfig(modelContextLength int) ManagerConfig {
	return ManagerConfig{
		ModelContextLength:  modelContextLength,
		ReservedTokens:      4096,
		CompressionConfig:   DefaultCompressorConfig(),
		CacheTTL:             5 * time.Minute,
	}
}

// LLMClient is the interface required for the compressor's summarization call.
// It is satisfied by pkg/model.LLMClient.
type LLMClient interface {
	Chat(ctx context.Context, req *model.ChatRequest) (*model.ChatResponse, error)
}

// contextClient wraps an LLMClient to satisfy the Summarizer interface.
type contextClient struct {
	client LLMClient
	model  string
}

// Summarize calls the LLM to produce a summary of the provided messages.
func (c *contextClient) Summarize(ctx context.Context, messages []*model.Message, systemPrompt string) (string, error) {
	msgs := make([]*model.Message, 0, len(messages)+1)
	msgs = append(msgs, model.SystemMessage(systemPrompt))
	msgs = append(msgs, messages...)
	req := &model.ChatRequest{
		Model:    c.model,
		Messages: msgs,
	}
	resp, err := c.client.Chat(ctx, req)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices returned from summarizer")
	}
	return resp.Choices[0].Message.Content, nil
}

// Manager orchestrates the context window: it tracks token usage, decides
// when to compress, caches system prompts, and assembles the message list
// for each LLM call.
type Manager struct {
	config   ManagerConfig
	logger   *slog.Logger
	cache    *TTLCache
	compressor *ContextCompressor
	client   LLMClient

	// Mutable state guarded by mu.
	mu                   sync.RWMutex
	messages             []*model.Message
	estimatedTotalTokens int
	lastPromptTokens     int
	compressionCount     int
	sessionID            string
}

// NewManager creates a new context window manager.
func NewManager(cfg ManagerConfig, logger *slog.Logger, llmClient LLMClient) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	cache := NewTTLCache(cfg.CacheTTL)
	// Use SummarizerModel if configured, otherwise fall back to "compact".
	summarizerModel := cfg.SummarizerModel
	if summarizerModel == "" {
		summarizerModel = "compact"
	}
	comp := NewContextCompressor(cfg.CompressionConfig, logger, &contextClient{client: llmClient, model: summarizerModel})
	return &Manager{
		config:       cfg,
		logger:       logger,
		cache:        cache,
		compressor:   comp,
		client:       llmClient,
		messages:     make([]*model.Message, 0),
	}
}

// AddMessage appends a message to the conversation history.
func (m *Manager) AddMessage(msg *model.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
	m.recomputeEstimateLocked()
}

// SetMessages replaces the entire message list (used after compression).
func (m *Manager) SetMessages(msgs []*model.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = msgs
	m.recomputeEstimateLocked()
}

// GetMessages returns the current message list (deep copy for safety).
func (m *Manager) GetMessages() []*model.Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*model.Message, len(m.messages))
	for i, msg := range m.messages {
		result[i] = msg.Clone()
	}
	return result
}

// GetMessagesForLLM returns the messages prepared for an LLM call, including\r
// checking whether compression is needed and applying it if triggered.
func (m *Manager) GetMessagesForLLM(ctx context.Context) ([]*model.Message, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.recomputeEstimateLocked()

	if !m.compressor.ShouldCompress(m.estimatedTotalTokens) {
		// Clone to prevent external mutation.
		result := make([]*model.Message, len(m.messages))
		copy(result, m.messages)
		return result, false, nil
	}

	compressed, err := m.compressor.Compress(m.messages, ctx)
	if err != nil {
		m.logger.Error("compression failed", "error", err)
		// Return uncompressed on error rather than failing the call.
		result := make([]*model.Message, len(m.messages))
		copy(result, m.messages)
		return result, false, nil
	}

	m.messages = compressed
	m.recomputeEstimateLocked()
	m.compressionCount++

	result := make([]*model.Message, len(m.messages))
	copy(result, m.messages)
	return result, true, nil
}

// ShouldCompress returns true if the current token estimate exceeds the threshold.
func (m *Manager) ShouldCompress() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.compressor.ShouldCompress(m.estimatedTotalTokens)
}

// Reset clears all messages and per-session state.
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = make([]*model.Message, 0)
	m.estimatedTotalTokens = 0
	m.lastPromptTokens = 0
	m.compressionCount = 0
	m.sessionID = ""
	m.compressor.Reset()
}

// SessionID returns the current session identifier.
func (m *Manager) SessionID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessionID
}

// SetSessionID sets the session identifier.
func (m *Manager) SetSessionID(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionID = id
}

// UpdateTokenUsage records actual token usage from the last LLM response.
func (m *Manager) UpdateTokenUsage(promptTokens, completionTokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastPromptTokens = promptTokens
}

// TokenBudget returns the current token budget state.
func (m *Manager) TokenBudget() TokenBudget {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return TokenBudget{
		MaxTokens:      m.config.ModelContextLength,
		UsedTokens:     m.estimatedTotalTokens,
		ReservedTokens: m.config.ReservedTokens,
	}
}

// CompressionStats returns compression statistics.
func (m *Manager) CompressionStats() (count int, savingsPct float64) {
	return m.compressor.CompressionStats()
}

// TotalTokens returns the estimated total token count for the current messages.
func (m *Manager) TotalTokens() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.estimatedTotalTokens
}

// recomputeEstimateLocked recalculates the token estimate.  Caller must hold mu.
func (m *Manager) recomputeEstimateLocked() {
	m.estimatedTotalTokens = EstimateMessagesTokens(m.messages)
}

// --------------------------------------------------------------------------
// System prompt caching (cache.go integration)
//
// GetCachedSystemPrompt returns a cached compiled system prompt for the given
// cache key, or "" if not found / expired.
// CacheSystemPrompt stores the compiled prompt for the given cache key.
// --------------------------------------------------------------------------+

// GetCachedSystemPrompt retrieves a cached system prompt by key.
// Returns (prompt, found).
func (m *Manager) GetCachedSystemPrompt(cacheKey Key) (string, bool) {
	fnvKey, shaHex := cacheKey.BuildKey()
	val, ok := m.cache.Get(fnvKey, shaHex)
	if !ok {
		return "", false
	}
	if s, ok := val.(string); ok {
		return s, true
	}
	return "", false
}

// CacheSystemPrompt stores a compiled system prompt in the cache.
func (m *Manager) CacheSystemPrompt(cacheKey Key, compiledPrompt string) {
	fnvKey, shaHex := cacheKey.BuildKey()
	m.cache.Set(fnvKey, shaHex, compiledPrompt)
}

// PurgeCache removes all cached system prompts.
func (m *Manager) PurgeCache() {
	m.cache.Purge()
}

// CacheStats returns cache statistics for observability.
func (m *Manager) CacheStats() Stats {
	return m.cache.Stats()
}

// --------------------------------------------------------------------------
// Message helpers
// --------------------------------------------------------------------------+

// EstimateMessagesTokens estimates total token count for a message list.
// Uses tiktoken cl100k_base when available (accurate), falls back to
// per-message EstimateMessageTokens heuristic.
func EstimateMessagesTokens(messages []*model.Message) int {
	if len(messages) == 0 {
		return 0
	}
	enc, err := getCl100kEncoder()
	if err != nil || enc == nil {
		// Fallback: use per-message heuristic.
		total := 0
		for _, m := range messages {
			total += EstimateMessageTokens(string(m.Role), m.Content, len(m.ToolCalls))
		}
		return total
	}

	// Fast tiktoken path: encode each message content with the shared encoder.
	total := 0
	for _, m := range messages {
		contentTokens := len(enc.Encode(m.Content, nil, nil))
		// Role delimiter overhead (~4 tokens).
		total += contentTokens + 4
		// Tool calls overhead (~15 tokens each).
		total += len(m.ToolCalls) * 15
	}
	return total
}

// FindMessageAtIndex is a search helper that returns the index of the message
// with the given role+content match, or -1.
func FindMessageAtIndex(messages []*model.Message, role model.Role, contentSubstring string) int {
	for i, msg := range messages {
		if msg.Role == role && strings.Contains(msg.Content, contentSubstring) {
			return i
		}
	}
	return -1
}

// TruncateMessages returns a slice of the most recent n messages.
func TruncateMessages(messages []*model.Message, n int) []*model.Message {
	if len(messages) <= n {
		return messages
	}
	result := make([]*model.Message, n)
	copy(result, messages[len(messages)-n:])
	return result
}

// String returns a human-readable description of the manager state.
func (m *Manager) String() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return fmt.Sprintf("ContextManager{tokens=%d, msgs=%d, compressions=%d, session=%q}",
		m.estimatedTotalTokens, len(m.messages), m.compressionCount, m.sessionID)
}

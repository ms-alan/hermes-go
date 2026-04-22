package context

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nousresearch/hermes-go/pkg/model"
)

// ---- mockLLMClient ----

type mockLLMClient struct {
	chunks []model.StreamChunk
	resp   *model.ChatResponse
	err    error
}

func (m *mockLLMClient) Chat(ctx context.Context, req *model.ChatRequest) (*model.ChatResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

func (m *mockLLMClient) Stream(ctx context.Context, req *model.ChatRequest) (<-chan model.StreamChunk, error) {
	if m.err != nil {
		ch := make(chan model.StreamChunk, 1)
		ch <- model.StreamChunk{Delta: model.Delta{Content: "error: mock"}}
		close(ch)
		return ch, nil
	}
	ch := make(chan model.StreamChunk, len(m.chunks))
	for _, c := range m.chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func (m *mockLLMClient) Close() error { return nil }

// ---- NewManager ----

func TestNewManager(t *testing.T) {
	t.Run("creates manager with defaults", func(t *testing.T) {
		cfg := DefaultManagerConfig(128000)
		logger := slog.Default()
		client := &mockLLMClient{resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}}}

		mgr := NewManager(cfg, logger, client)
		if mgr == nil {
			t.Fatal("NewManager returned nil")
		}
		if mgr.config.ModelContextLength != 128000 {
			t.Errorf("ModelContextLength = %d, want 128000", mgr.config.ModelContextLength)
		}
		if mgr.config.ReservedTokens != 4096 {
			t.Errorf("ReservedTokens = %d, want 4096", mgr.config.ReservedTokens)
		}
	})

	t.Run("uses default logger if nil", func(t *testing.T) {
		cfg := DefaultManagerConfig(128000)
		mgr := NewManager(cfg, nil, &mockLLMClient{resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}}})
		if mgr == nil {
			t.Fatal("NewManager returned nil")
		}
	})
}

// ---- AddMessage ----

func TestAddMessage(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	mgr.AddMessage(model.UserMessage("Hello"))
	mgr.AddMessage(model.SystemMessage("You are helpful"))

	msgs := mgr.GetMessages()
	if len(msgs) != 2 {
		t.Errorf("len(messages) = %d, want 2", len(msgs))
	}
	if msgs[0].Role != model.RoleUser {
		t.Errorf("msg[0].Role = %s, want user", msgs[0].Role)
	}
	if msgs[1].Role != model.RoleSystem {
		t.Errorf("msg[1].Role = %s, want system", msgs[1].Role)
	}
}

// ---- SetMessages ----

func TestSetMessages(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	mgr.AddMessage(model.UserMessage("one"))
	newMsgs := []*model.Message{
		model.UserMessage("replaced"),
		{Role: model.RoleAssistant, Content: "response"},
	}
	mgr.SetMessages(newMsgs)

	msgs := mgr.GetMessages()
	if len(msgs) != 2 {
		t.Errorf("len(messages) after SetMessages = %d, want 2", len(msgs))
	}
	if msgs[0].Content != "replaced" {
		t.Errorf("msg[0].Content = %q, want %q", msgs[0].Content, "replaced")
	}
}

// ---- GetMessages ----

func TestGetMessages_ReturnsCopy(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	mgr.AddMessage(model.UserMessage("original"))

	msgs1 := mgr.GetMessages()
	msgs1[0].Content = "modified"

	msgs2 := mgr.GetMessages()
	if msgs2[0].Content != "original" {
		t.Errorf("GetMessages should return copy; got %q", msgs2[0].Content)
	}
}

// ---- GetMessagesForLLM ----

func TestGetMessagesForLLM_NoCompression(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	// Use a very high threshold percent so compression never triggers
	cfg.CompressionConfig.ThresholdPercent = 100.0 // 100% threshold = never compress
	cfg.CompressionConfig.QuietMode = true
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	mgr.AddMessage(model.UserMessage("hello"))
	mgr.AddMessage(&model.Message{Role: model.RoleAssistant, Content: "hi there"})

	ctx := context.Background()
	msgs, compressed, err := mgr.GetMessagesForLLM(ctx)
	if err != nil {
		t.Fatalf("GetMessagesForLLM: %v", err)
	}
	if compressed {
		t.Error("compressed = true, want false")
	}
	if len(msgs) != 2 {
		t.Errorf("len(msgs) = %d, want 2", len(msgs))
	}
}

// ---- ShouldCompress ----

func TestShouldCompress(t *testing.T) {
	cfg := DefaultManagerConfig(1000)
	cfg.CompressionConfig.ThresholdPercent = 0.05 // 5% of 1000 = 50 tokens threshold
	cfg.CompressionConfig.QuietMode = true
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	// With threshold at 50 tokens, a short message should not trigger
	if mgr.ShouldCompress() {
		t.Error("ShouldCompress = true, want false with low token count")
	}
}

// ---- Reset ----

func TestReset(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	mgr.AddMessage(model.UserMessage("hello"))
	mgr.SetSessionID("session-123")

	mgr.Reset()

	msgs := mgr.GetMessages()
	if len(msgs) != 0 {
		t.Errorf("messages after Reset = %d, want 0", len(msgs))
	}
	if mgr.SessionID() != "" {
		t.Errorf("sessionID after Reset = %q, want %q", mgr.SessionID(), "")
	}
	if mgr.TotalTokens() != 0 {
		t.Errorf("TotalTokens after Reset = %d, want 0", mgr.TotalTokens())
	}
}

// ---- SessionID / SetSessionID ----

func TestSessionID(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	if mgr.SessionID() != "" {
		t.Errorf("initial SessionID = %q, want %q", mgr.SessionID(), "")
	}

	mgr.SetSessionID("my-session")
	if mgr.SessionID() != "my-session" {
		t.Errorf("SessionID = %q, want %q", mgr.SessionID(), "my-session")
	}
}

// ---- UpdateTokenUsage ----

func TestUpdateTokenUsage(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	mgr.UpdateTokenUsage(150, 50)
	// TokenBudget recomputes from messages, not stored separately
	// Just verify it doesn't panic
}

// ---- TokenBudget ----

func TestTokenBudget(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	cfg.ReservedTokens = 5000
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	budget := mgr.TokenBudget()
	if budget.MaxTokens != 128000 {
		t.Errorf("MaxTokens = %d, want 128000", budget.MaxTokens)
	}
	if budget.ReservedTokens != 5000 {
		t.Errorf("ReservedTokens = %d, want 5000", budget.ReservedTokens)
	}
}

// ---- TotalTokens ----

func TestTotalTokens(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	if mgr.TotalTokens() != 0 {
		t.Errorf("TotalTokens for empty = %d, want 0", mgr.TotalTokens())
	}

	mgr.AddMessage(model.UserMessage("hello"))
	tokens := mgr.TotalTokens()
	if tokens == 0 {
		t.Error("TotalTokens should be > 0 after adding message")
	}
}

// ---- CompressionStats ----

func TestCompressionStats(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	count, savings := mgr.CompressionStats()
	if count != 0 {
		t.Errorf("initial compression count = %d, want 0", count)
	}
	if savings != 0 {
		t.Errorf("initial savings = %f, want 0", savings)
	}
}

// ---- System Prompt Cache ----

func TestCacheSystemPrompt(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	cfg.CacheTTL = 10 * time.Second
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	key := Key{SystemPrompt: "You are helpful", Model: "compact"}
	mgr.CacheSystemPrompt(key, "cached prompt string")

	cached, ok := mgr.GetCachedSystemPrompt(key)
	if !ok {
		t.Error("expected to find cached system prompt")
	}
	if cached != "cached prompt string" {
		t.Errorf("cached = %q, want %q", cached, "cached prompt string")
	}
}

func TestCacheSystemPrompt_NotFound(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	_, ok := mgr.GetCachedSystemPrompt(Key{})
	if ok {
		t.Error("expected not to find cache for empty key")
	}
}

func TestPurgeCache(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	cfg.CacheTTL = 10 * time.Second
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	key := Key{SystemPrompt: "You are helpful", Model: "compact"}
	mgr.CacheSystemPrompt(key, "cached prompt")
	mgr.PurgeCache()

	_, ok := mgr.GetCachedSystemPrompt(key)
	if ok {
		t.Error("expected cache to be purged")
	}
}

func TestCacheStats(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	cfg.CacheTTL = 10 * time.Second
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	stats := mgr.CacheStats()
	// Just verify it doesn't panic
	_ = stats.Items
	_ = stats.AvgTTLSecs
	_ = stats.OldestEntry
}

// ---- EstimateMessagesTokens ----

func TestEstimateMessagesTokens(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.RoleSystem, Content: "You are helpful"},
		{Role: model.RoleUser, Content: "Hello"},
		{Role: model.RoleAssistant, Content: "Hi there"},
	}
	tokens := EstimateMessagesTokens(msgs)
	if tokens == 0 {
		t.Error("EstimateMessagesTokens returned 0, expected > 0")
	}
}

// ---- FindMessageAtIndex ----

func TestFindMessageAtIndex(t *testing.T) {
	msgs := []*model.Message{
		{Role: model.RoleSystem, Content: "You are helpful"},
		{Role: model.RoleUser, Content: "What is Go?"},
		{Role: model.RoleAssistant, Content: "Go is a programming language."},
	}

	idx := FindMessageAtIndex(msgs, model.RoleUser, "What is Go")
	if idx != 1 {
		t.Errorf("FindMessageAtIndex(user, What is Go) = %d, want 1", idx)
	}

	idx = FindMessageAtIndex(msgs, model.RoleAssistant, "programming language")
	if idx != 2 {
		t.Errorf("FindMessageAtIndex(assistant, programming language) = %d, want 2", idx)
	}

	idx = FindMessageAtIndex(msgs, model.RoleTool, "something")
	if idx != -1 {
		t.Errorf("FindMessageAtIndex(tool, something) = %d, want -1", idx)
	}

	idx = FindMessageAtIndex(msgs, model.RoleUser, "not present")
	if idx != -1 {
		t.Errorf("FindMessageAtIndex(user, not present) = %d, want -1", idx)
	}
}

// ---- TruncateMessages ----

func TestTruncateMessages(t *testing.T) {
	msgs := make([]*model.Message, 10)
	for i := 0; i < 10; i++ {
		msgs[i] = model.UserMessage("msg")
	}

	truncated := TruncateMessages(msgs, 3)
	if len(truncated) != 3 {
		t.Errorf("len(TruncateMessages(10, 3)) = %d, want 3", len(truncated))
	}
	if len(msgs) != 10 {
		t.Error("original msgs should not be modified")
	}

	// Less than n returns same slice
	truncated = TruncateMessages(msgs, 20)
	if len(truncated) != 10 {
		t.Errorf("len(TruncateMessages(10, 20)) = %d, want 10", len(truncated))
	}
}

// ---- String ----

func TestManagerString(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	mgr := NewManager(cfg, slog.Default(), &mockLLMClient{
		resp: &model.ChatResponse{Choices: []model.Choice{{Message: &model.Message{Content: "ok"}}}},
	})

	mgr.SetSessionID("test-session")
	mgr.AddMessage(model.UserMessage("hello"))

	s := mgr.String()
	if s == "" {
		t.Error("String() returned empty")
	}
}

// ---- DefaultManagerConfig ----

func TestDefaultManagerConfig(t *testing.T) {
	cfg := DefaultManagerConfig(128000)
	if cfg.ModelContextLength != 128000 {
		t.Errorf("ModelContextLength = %d, want 128000", cfg.ModelContextLength)
	}
	if cfg.ReservedTokens != 4096 {
		t.Errorf("ReservedTokens = %d, want 4096", cfg.ReservedTokens)
	}
	if cfg.CacheTTL != 5*time.Minute {
		t.Errorf("CacheTTL = %v, want 5m", cfg.CacheTTL)
	}
}

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/nousresearch/hermes-go/pkg/memory"
	"github.com/nousresearch/hermes-go/pkg/model"
	"github.com/nousresearch/hermes-go/pkg/session"
	ctxmgr "github.com/nousresearch/hermes-go/pkg/context"
)

// SessionAgent wraps an AIAgent with session persistence and context management.
type SessionAgent struct {
	agent      *AIAgent
	store      *session.Store
	ctxMgr     *ctxmgr.Manager
	memMgr     *memory.MemoryManager
	logger     *slog.Logger
	sessID     string
	sessInfo   *session.Session
	turnCount  int // increments each Chat call, used for OnTurnStart
}

// NewSessionAgent creates a new SessionAgent with the given components.
// If memMgr is nil, memory features are disabled (graceful degradation).
func NewSessionAgent(agent *AIAgent, store *session.Store, ctxMgr *ctxmgr.Manager, memMgr *memory.MemoryManager, logger *slog.Logger) *SessionAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionAgent{
		agent:  agent,
		store:  store,
		ctxMgr: ctxMgr,
		memMgr: memMgr,
		logger: logger,
	}
}

// Chat processes a user message within the current session.
// It adds the user message to the context manager, runs the agent,
// and persists all messages to the session store.
//
// Memory lifecycle (per turn):
//   - OnTurnStart → inject prefetch context → RunWithMessages → SyncAll → QueuePrefetchAll
func (sa *SessionAgent) Chat(ctx context.Context, userMsg string) (string, error) {
	if sa.sessID == "" {
		return "", fmt.Errorf("no active session: use New() or Switch() first")
	}

	sa.turnCount++

	// Add user message to context manager
	sa.ctxMgr.AddMessage(model.UserMessage(userMsg))

	// Notify memory providers of turn start
	if sa.memMgr != nil {
		sa.memMgr.OnTurnStart(sa.turnCount, userMsg, map[string]any{
			"platform": "cli",
		})
	}

	// Get messages for LLM (may trigger compression)
	msgsForLLM, compressed, err := sa.ctxMgr.GetMessagesForLLM(ctx)
	if err != nil {
		sa.logger.Warn("GetMessagesForLLM failed", "error", err)
		msgsForLLM = sa.ctxMgr.GetMessages()
		compressed = false
	}

	if compressed {
		sa.logger.Info("context was compressed before LLM call")
		// Notify memory providers before compression
		if sa.memMgr != nil {
			sa.notifyPreCompress()
		}
	}

	// Build system prompt: stored prompt + memory snapshot
	systemPrompt := sa.buildSystemPrompt()

	// Inject prefetch context from memory providers (after system prompt, before user message)
	// This adds a <memory-context> block with recalled memories
	if sa.memMgr != nil {
		prefetch := sa.memMgr.PrefetchAll(userMsg, sa.sessID)
		if prefetch != "" {
			memContext := memory.BuildMemoryContextBlock(prefetch)
			// Prepend to the last user message for this turn
			if len(msgsForLLM) > 0 && msgsForLLM[len(msgsForLLM)-1].Role == model.RoleUser {
				lastUser := msgsForLLM[len(msgsForLLM)-1]
				lastUser.Content = memContext + "\n\n" + lastUser.Content
			}
		}
	}

	result := sa.agent.RunWithMessages(ctx, msgsForLLM, systemPrompt)
	if result.Error != nil {
		return "", result.Error
	}

	// Sync turn to memory providers
	if sa.memMgr != nil {
		// Collect user and assistant content from this turn
		userContent := userMsg
		assistantContent := result.FinalResponse
		sa.memMgr.SyncAll(userContent, assistantContent, sa.sessID)
		sa.memMgr.QueuePrefetchAll(userMsg, sa.sessID)
	}

	// Update context manager with the new messages from the result
	for _, msg := range result.Messages[len(msgsForLLM):] {
		sa.ctxMgr.AddMessage(msg)
	}

	// Persist all messages to the store
	for _, msg := range result.Messages {
		storeMsg := sa.toStoreMessage(msg)
		if _, err := sa.store.AppendMessage(storeMsg); err != nil {
			sa.logger.Warn("failed to persist message", "error", err)
		}
	}

	return result.FinalResponse, nil
}

// notifyPreCompress collects pre-compression summaries from all memory providers.
// Called when context compression is about to discard messages.
func (sa *SessionAgent) notifyPreCompress() {
	if sa.memMgr == nil {
		return
	}
	messages := make([]map[string]any, 0, len(sa.ctxMgr.GetMessages()))
	for _, m := range sa.ctxMgr.GetMessages() {
		messages = append(messages, map[string]any{
			"role":    string(m.Role),
			"content": m.Content,
		})
	}
	sa.memMgr.OnPreCompress(messages)
}

// buildSystemPrompt assembles the system prompt from stored prompt + memory snapshot.
func (sa *SessionAgent) buildSystemPrompt() string {
	var parts []string

	// Base system prompt from session store
	if sa.sessInfo != nil && sa.sessInfo.SystemPrompt != nil && *sa.sessInfo.SystemPrompt != "" {
		parts = append(parts, *sa.sessInfo.SystemPrompt)
	}

	// Memory system prompt blocks from all providers
	if sa.memMgr != nil {
		memPrompt := sa.memMgr.BuildSystemPrompt()
		if memPrompt != "" {
			parts = append(parts, memPrompt)
		}
	} else if memStore := memory.GetMemoryStore(); memStore != nil {
		// Fallback: direct memory store access (for backward compat / nil memMgr)
		memBlock, userBlock := memStore.FrozenSnapshot()
		if memBlock != "" {
			parts = append(parts, memBlock)
		}
		if userBlock != "" {
			parts = append(parts, userBlock)
		}
	}

	return strings.Join(parts, "\n\n")
}

// New creates a new session and sets it as the current active session.
func (sa *SessionAgent) New(source, model string, systemPrompt string) (string, error) {
	sessID := uuid.New().String()
	if _, err := sa.store.CreateSession(sessID, source, model, nil, systemPrompt, "", ""); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	sa.sessID = sessID
	sa.ctxMgr.Reset()
	sa.ctxMgr.SetSessionID(sessID)
	sa.turnCount = 0

	sess, err := sa.store.GetSession(sessID)
	if err != nil {
		return sessID, nil
	}
	sa.sessInfo = sess
	return sessID, nil
}

// Switch sets the active session by ID, loading its messages into the context manager.
func (sa *SessionAgent) Switch(sessionID string) error {
	// Resolve prefix to full ID if needed
	actualID, err := sa.store.ResolveSessionID(sessionID)
	if err != nil {
		return fmt.Errorf("resolve session ID: %w", err)
	}
	if actualID == "" {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	sess, err := sa.store.GetSession(actualID)
	if err != nil {
		return fmt.Errorf("get session: %w", err)
	}
	if sess == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	sa.sessID = actualID
	sa.sessInfo = sess
	sa.ctxMgr.Reset()
	sa.ctxMgr.SetSessionID(actualID)
	sa.turnCount = 0

	// Load existing messages into context manager
	msgs, err := sa.store.GetMessages(actualID)
	if err != nil {
		sa.logger.Warn("failed to load session messages", "error", err)
		return nil
	}
	for _, m := range msgs {
		msg := sa.fromStoreMessage(m)
		if msg != nil {
			sa.ctxMgr.AddMessage(msg)
		}
	}

	// Notify memory providers of session end (for the switched-away session, not applicable)
	// and start fresh for the new session
	return nil
}

// Sessions returns a list of all sessions.
func (sa *SessionAgent) Sessions(limit, offset int) ([]*session.Session, error) {
	if limit <= 0 {
		limit = 20
	}
	return sa.store.ListSessions("", limit, offset)
}

// CurrentSession returns the current active session.
func (sa *SessionAgent) CurrentSession() *session.Session {
	return sa.sessInfo
}

// SessionID returns the current session ID.
func (sa *SessionAgent) SessionID() string {
	return sa.sessID
}

// Search performs a full-text search across session messages.
func (sa *SessionAgent) Search(query string, limit, offset int) ([]session.SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	return sa.store.Search(session.SearchOptions{
		Query:  query,
		Limit:  limit,
		Offset: offset,
	})
}

// MemoryManager returns the memory manager (nil if not configured).
func (sa *SessionAgent) MemoryManager() *memory.MemoryManager {
	return sa.memMgr
}

// OnSessionEnd notifies all memory providers that the session has ended.
func (sa *SessionAgent) OnSessionEnd() {
	if sa.memMgr == nil {
		return
	}
	messages := make([]map[string]any, 0, len(sa.ctxMgr.GetMessages()))
	for _, m := range sa.ctxMgr.GetMessages() {
		messages = append(messages, map[string]any{
			"role":    string(m.Role),
			"content": m.Content,
		})
	}
	sa.memMgr.OnSessionEnd(messages)
}

// OnDelegation notifies memory providers that a subagent completed.
// Corresponds to hermes-agent's MemoryManager.on_delegation().
func (sa *SessionAgent) OnDelegation(task string, result string, childSessionID string) {
	if sa.memMgr == nil {
		return
	}
	sa.memMgr.OnDelegation(task, result, childSessionID)
}

// OnMemoryWrite notifies external memory providers when the built-in memory
// tool writes an entry (add/replace/remove).
func (sa *SessionAgent) OnMemoryWrite(action string, target string, content string) {
	if sa.memMgr == nil {
		return
	}
	sa.memMgr.OnMemoryWrite(action, target, content)
}

// Shutdown gracefully shuts down the session agent and its memory providers.
func (sa *SessionAgent) Shutdown() {
	if sa.memMgr != nil {
		sa.memMgr.ShutdownAll()
	}
}

// toStoreMessage converts a model.Message to a session.Message.
func (sa *SessionAgent) toStoreMessage(msg *model.Message) *session.Message {
	storeMsg := &session.Message{
		SessionID: sa.sessID,
		Role:      string(msg.Role),
		Content:   &msg.Content,
	}
	if msg.ToolCallID != "" {
		storeMsg.ToolCallID = &msg.ToolCallID
	}
	if len(msg.ToolCalls) > 0 {
		if tcJSON, err := json.Marshal(msg.ToolCalls); err == nil {
			storeMsg.ToolCalls = tcJSON
		}
	}
	return storeMsg
}

// fromStoreMessage converts a session.Message to a model.Message.
func (sa *SessionAgent) fromStoreMessage(msg *session.Message) *model.Message {
	if msg == nil {
		return nil
	}
	modelMsg := &model.Message{
		Role: model.Role(msg.Role),
	}
	if msg.Content != nil {
		modelMsg.Content = *msg.Content
	}
	if msg.ToolCallID != nil {
		modelMsg.ToolCallID = *msg.ToolCallID
	}
	if len(msg.ToolCalls) > 0 {
		var toolCalls []*model.ToolCall
		if err := json.Unmarshal(msg.ToolCalls, &toolCalls); err == nil {
			modelMsg.ToolCalls = toolCalls
		}
	}
	return modelMsg
}

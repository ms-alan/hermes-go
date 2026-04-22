package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/nousresearch/hermes-go/pkg/model"
	"github.com/nousresearch/hermes-go/pkg/session"
	ctxmgr "github.com/nousresearch/hermes-go/pkg/context"
)

// SessionAgent wraps an AIAgent with session persistence and context management.
type SessionAgent struct {
	agent    *AIAgent
	store    *session.Store
	ctxMgr   *ctxmgr.Manager
	logger   *slog.Logger
	sessID   string
	sessInfo *session.Session
}

// NewSessionAgent creates a new SessionAgent with the given components.
func NewSessionAgent(agent *AIAgent, store *session.Store, ctxMgr *ctxmgr.Manager, logger *slog.Logger) *SessionAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionAgent{
		agent:  agent,
		store:  store,
		ctxMgr: ctxMgr,
		logger: logger,
	}
}

// Chat processes a user message within the current session.
// It adds the user message to the context manager, runs the agent,
// and persists all messages to the session store.
func (sa *SessionAgent) Chat(ctx context.Context, userMsg string) (string, error) {
	if sa.sessID == "" {
		return "", fmt.Errorf("no active session: use New() or Switch() first")
	}

	// Add user message to context manager
	sa.ctxMgr.AddMessage(model.UserMessage(userMsg))

	// Get messages for LLM (may trigger compression)
	msgsForLLM, compressed, err := sa.ctxMgr.GetMessagesForLLM(ctx)
	if err != nil {
		sa.logger.Warn("GetMessagesForLLM failed", "error", err)
		msgsForLLM = sa.ctxMgr.GetMessages()
		compressed = false
	}

	if compressed {
		sa.logger.Info("context was compressed before LLM call")
	}

	// Run the agent with the prepared messages
	systemPrompt := ""
	if sa.sessInfo != nil && sa.sessInfo.SystemPrompt != nil {
		systemPrompt = *sa.sessInfo.SystemPrompt
	}
	result := sa.agent.RunWithMessages(ctx, msgsForLLM, systemPrompt)
	if result.Error != nil {
		return "", result.Error
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

// New creates a new session and sets it as the current active session.
func (sa *SessionAgent) New(source, model string, systemPrompt string) (string, error) {
	sessID := uuid.New().String()
	if _, err := sa.store.CreateSession(sessID, source, model, nil, systemPrompt, "", ""); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	sa.sessID = sessID
	sa.ctxMgr.Reset()
	sa.ctxMgr.SetSessionID(sessID)

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

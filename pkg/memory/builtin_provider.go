package memory

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// BuiltinMemoryProvider wraps the built-in MemoryStore as a MemoryProvider.
// It is always registered first and cannot be removed.
// This corresponds to hermes-agent's BuiltinMemoryProvider.
type BuiltinMemoryProvider struct {
	ProviderBase
	store *MemoryStore
}

// NewBuiltinMemoryProvider creates a BuiltinMemoryProvider with the given store.
func NewBuiltinMemoryProvider(store *MemoryStore) *BuiltinMemoryProvider {
	return &BuiltinMemoryProvider{
		ProviderBase: DefaultProviderBase(),
		store:        store,
	}
}

// NewBuiltinMemoryProviderDefault creates a provider with a default global store.
func NewBuiltinMemoryProviderDefault() *BuiltinMemoryProvider {
	return NewBuiltinMemoryProvider(Global())
}

// Name implements MemoryProvider.
func (p *BuiltinMemoryProvider) Name() string { return "builtin" }

// IsAvailable implements MemoryProvider — always available if store is non-nil.
func (p *BuiltinMemoryProvider) IsAvailable() bool { return p.store != nil }

// Initialize implements MemoryProvider — loads the store from disk.
func (p *BuiltinMemoryProvider) Initialize(sessionID string, hermesHome string, kwargs map[string]any) error {
	if p.store == nil {
		return fmt.Errorf("builtin memory provider: store is nil")
	}
	return p.store.Load()
}

// SystemPromptBlock implements MemoryProvider.
// Returns the frozen memory + user snapshot formatted as a system prompt block.
func (p *BuiltinMemoryProvider) SystemPromptBlock() string {
	if p.store == nil {
		return ""
	}
	memBlock, userBlock := p.store.FrozenSnapshot()
	var parts []string
	if memBlock != "" {
		parts = append(parts, memBlock)
	}
	if userBlock != "" {
		parts = append(parts, userBlock)
	}
	if len(parts) == 0 {
		return ""
	}
	return parts[0] + "\n\n" + parts[1]
}

// SystemPromptBlockForMemory returns just the memory block (for separate labeling).
func (p *BuiltinMemoryProvider) SystemPromptBlockForMemory() string {
	if p.store == nil {
		return ""
	}
	block, _ := p.store.FrozenSnapshot()
	return block
}

// SystemPromptBlockForUser returns just the user block (for separate labeling).
func (p *BuiltinMemoryProvider) SystemPromptBlockForUser() string {
	if p.store == nil {
		return ""
	}
	_, block := p.store.FrozenSnapshot()
	return block
}

// Prefetch implements MemoryProvider — built-in store has no recall.
func (p *BuiltinMemoryProvider) Prefetch(query string, sessionID string) string {
	return ""
}

// SyncTurn implements MemoryProvider — built-in store uses direct writes.
func (p *BuiltinMemoryProvider) SyncTurn(userContent string, assistantContent string, sessionID string) {
	// Built-in memory uses direct tool calls (memory add/replace/remove).
	// No per-turn background sync needed.
}

// memoryToolSchema is the OpenAI tool schema for the built-in memory tool.
var memoryToolSchema = ToolSchema{
	"name": "memory",
	"description": "Persistent curated memory for the agent — two stores: MEMORY.md (agent's personal notes) and USER.md (user profile). Use 'add' to save new facts/preferences as entries. Use 'replace' or 'remove' to update entries using unique substring matching. Mid-session writes persist to disk immediately but do NOT change the system prompt until the next session. Character limits enforced: MEMORY 2,200 chars, USER 1,375 chars. Entries separated by §.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action: 'add' (append entry), 'replace' (replace matching entry), 'remove' (delete matching entry), 'read' (show entries)",
				"enum":        []any{"add", "replace", "remove", "read"},
			},
			"target": map[string]any{
				"type":        "string",
				"description": "Memory store: 'memory' (agent notes) or 'user' (user profile)",
				"enum":        []any{"memory", "user"},
				"default":     "memory",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content for add/replace actions. Required for add and replace. Should be a complete, self-contained entry.",
			},
			"old_text": map[string]any{
				"type":        "string",
				"description": "Unique substring to match for replace/remove. Must match exactly one entry.",
			},
		},
		"required": []any{"action"},
	},
}

// GetToolSchemas implements MemoryProvider.
func (p *BuiltinMemoryProvider) GetToolSchemas() []ToolSchema {
	return []ToolSchema{memoryToolSchema}
}

// HandleToolCall implements MemoryProvider.
func (p *BuiltinMemoryProvider) HandleToolCall(toolName string, args map[string]any) string {
	if toolName != "memory" {
		return fmt.Sprintf(`{"error": "builtin provider does not handle tool '%s'"}`, toolName)
	}
	if p.store == nil {
		return `{"error": "memory store not initialized"}`
	}

	action, _ := args["action"].(string)
	target, _ := args["target"].(string)
	if target == "" {
		target = "memory"
	}
	content, _ := args["content"].(string)
	oldText, _ := args["old_text"].(string)

	var result Action
	switch action {
	case "add":
		result = p.store.Add(target, content)
	case "replace":
		result = p.store.Replace(target, oldText, content)
	case "remove":
		result = p.store.Remove(target, oldText)
	case "read":
		result = p.store.Read(target)
	default:
		return fmt.Sprintf(`{"error": "unknown action '%s': must be add, replace, remove, or read"}`, action)
	}

	resp, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf(`{"error": "failed to marshal result: %v"}`, err)
	}
	return string(resp)
}

// Shutdown implements MemoryProvider — no-op for built-in.
func (p *BuiltinMemoryProvider) Shutdown() {}

// GetStore returns the underlying MemoryStore.
func (p *BuiltinMemoryProvider) GetStore() *MemoryStore { return p.store }

// WithBuiltinProvider is a convenience method on MemoryManager to add the
// built-in provider. Provided here for ergonomic use from cmd/hermes.
func (m *MemoryManager) WithBuiltinProvider(store *MemoryStore) {
	m.AddProvider(NewBuiltinMemoryProvider(store))
}

// MemoryStoreLogger returns the memory store's logger (nil if unavailable).
// Used by manager for error logging.
func (p *BuiltinMemoryProvider) Logger() *slog.Logger {
	return p.logger
}

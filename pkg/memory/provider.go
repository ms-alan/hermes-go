// Package memory provides persistent curated memory with a pluggable provider
// architecture. The BuiltinMemoryProvider is always active. External providers
// (Honcho, Mem0, etc.) can be added via MemoryManager but only one is allowed
// at a time to prevent tool schema bloat.
package memory

import (
	"log/slog"
)

// ToolSchema represents an OpenAI-function-calling tool schema.
type ToolSchema map[string]any

// ConfigField describes a single config field for memory setup.
type ConfigField struct {
	Key         string   // config key name (e.g. "api_key")
	Description string   // human-readable description
	Secret      bool     // true if this should go to .env
	Required    bool     // true if required
	Default     string   // default value
	Choices     []string // valid values (optional)
	URL         string   // URL where user can get this credential
	EnvVar      string   // explicit env var name for secrets
}

// MemoryProvider is the interface for pluggable memory backends.
//
// Built-in memory (MEMORY.md / USER.md) is always active as the first provider
// and cannot be removed. External providers are additive — they never disable
// the built-in store. Only one external provider runs at a time.
//
// Lifecycle (called by MemoryManager):
//   - Initialize()   — connect, create resources, warm up
//   - SystemPromptBlock() — static text for the system prompt
//   - Prefetch()     — background recall before each turn
//   - SyncTurn()     — async write after each turn
//   - GetToolSchemas() — tool schemas to expose to the model
//   - HandleToolCall() — dispatch a tool call
//   - Shutdown()     — clean exit
//
// Optional hooks (override to opt in):
//   - OnTurnStart()      — per-turn tick with runtime context
//   - OnSessionEnd()     — end-of-session extraction
//   - OnPreCompress()    — extract before context compression
//   - OnMemoryWrite()    — mirror built-in memory writes
//   - OnDelegation()     — parent-side observation of subagent work
type MemoryProvider interface {
	// Name returns a short identifier (e.g. "builtin", "honcho", "mem0").
	Name() string

	// IsAvailable returns true if this provider is configured and ready.
	// Called during agent init. Should not make network calls.
	IsAvailable() bool

	// Initialize sets up the provider for a session.
	// sessionID is the current session ID.
	// kwargs may include: hermesHome, platform, agentContext, agentIdentity,
	// agentWorkspace, parentSessionID, userID.
	Initialize(sessionID string, hermesHome string, kwargs map[string]any) error

	// SystemPromptBlock returns text to include in the system prompt.
	// Called during system prompt assembly. Return empty string to skip.
	// For STATIC provider info only. Prefetched recall uses Prefetch().
	SystemPromptBlock() string

	// Prefetch recalls relevant context for the upcoming turn.
	// Return formatted text to inject as context, or empty string.
	// Implementations should be fast — use background threads for
	// the actual recall and return cached results.
	Prefetch(query string, sessionID string) string

	// QueuePrefetch queues a background recall for the NEXT turn.
	// Called after each turn completes. Default is no-op.
	QueuePrefetch(query string, sessionID string)

	// SyncTurn persists a completed turn to the backend.
	// Should be non-blocking — queue for background processing.
	SyncTurn(userContent string, assistantContent string, sessionID string)

	// GetToolSchemas returns tool schemas this provider exposes.
	// Each schema follows OpenAI function calling format.
	// Return empty slice if this provider has no tools.
	GetToolSchemas() []ToolSchema

	// HandleToolCall handles a tool call for one of this provider's tools.
	// Must return a JSON string (the tool result).
	// Only called for tool names returned by GetToolSchemas().
	HandleToolCall(toolName string, args map[string]any) string

	// Shutdown performs clean shutdown — flush queues, close connections.
	Shutdown()

	// ---- Optional hooks (override to opt in) -------------------------------

	// OnTurnStart is called at the start of each turn with the user message.
	// Use for turn-counting, scope management, periodic maintenance.
	// kwargs may include: remainingTokens, model, platform, toolCount.
	OnTurnStart(turnNumber int, message string, kwargs map[string]any)

	// OnSessionEnd is called when a session ends (explicit exit or timeout).
	// Use for end-of-session fact extraction, summarization.
	// messages is the full conversation history.
	OnSessionEnd(messages []map[string]any)

	// OnPreCompress is called before context compression discards old messages.
	// Use to extract insights from messages about to be compressed.
	// Return text to include in the compression summary prompt.
	OnPreCompress(messages []map[string]any) string

	// OnMemoryWrite is called when the built-in memory tool writes an entry.
	// Use to mirror built-in memory writes to your backend.
	// action: "add", "replace", or "remove"; target: "memory" or "user".
	OnMemoryWrite(action string, target string, content string)

	// OnDelegation is called on the PARENT agent when a subagent completes.
	// task: the delegation prompt; result: the subagent's final response.
	OnDelegation(task string, result string, childSessionID string)

	// GetConfigSchema returns config fields this provider needs for setup.
	// Used by 'hermes memory setup'. Return empty slice if no config needed.
	GetConfigSchema() []ConfigField

	// SaveConfig writes non-secret config to the provider's native location.
	// Called by 'hermes memory setup' after collecting user inputs.
	// values contains only non-secret fields.
	SaveConfig(values map[string]any, hermesHome string) error
}

// ProviderBase provides default no-op implementations for optional hooks.
// Embed this in providers to avoid implementing every optional method.
type ProviderBase struct {
	logger *slog.Logger
}

// DefaultProviderBase returns a ProviderBase with the standard logger.
func DefaultProviderBase() ProviderBase {
	return ProviderBase{logger: slog.Default()}
}

// WithLogger sets a custom logger on the base.
func (pb ProviderBase) WithLogger(logger *slog.Logger) ProviderBase {
	pb.logger = logger
	return pb
}

func (pb ProviderBase) OnTurnStart(turnNumber int, message string, kwargs map[string]any)            {}
func (pb ProviderBase) OnSessionEnd(messages []map[string]any)                                       {}
func (pb ProviderBase) OnPreCompress(messages []map[string]any) string                                { return "" }
func (pb ProviderBase) OnMemoryWrite(action string, target string, content string)                    {}
func (pb ProviderBase) OnDelegation(task string, result string, childSessionID string)                {}
func (pb ProviderBase) GetConfigSchema() []ConfigField                                              { return nil }
func (pb ProviderBase) SaveConfig(values map[string]any, hermesHome string) error                  { return nil }
func (pb ProviderBase) QueuePrefetch(query string, sessionID string)                                 {}

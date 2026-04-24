// Package prompt provides structured system-prompt construction for Hermes agents.
// It supports slot-based composition (SOUL, memory, project context, platform identity)
// and is used by both the REPL and the gateway.
package prompt

import (
	"strings"

	hermescontext "github.com/nousresearch/hermes-go/pkg/context"
	hermesmemory "github.com/nousresearch/hermes-go/pkg/memory"
)

// Builder assembles a system prompt from configurable components (slots).
type Builder struct {
	ctxLoader *hermescontext.Loader
	memMgr    *hermesmemory.MemoryManager
	platform  string // "cli", "qq", "telegram", "discord", "cron"
	extras    []string
}

// NewBuilder creates a prompt builder with the given context loader and memory manager.
func NewBuilder(ctxLoader *hermescontext.Loader, memMgr *hermesmemory.MemoryManager) *Builder {
	return &Builder{
		ctxLoader: ctxLoader,
		memMgr:    memMgr,
	}
}

// WithPlatform sets the platform label (used for platform-specific identity).
func (b *Builder) WithPlatform(platform string) *Builder {
	b.platform = platform
	return b
}

// WithExtra appends an arbitrary string as an additional prompt segment.
func (b *Builder) WithExtra(segment string) *Builder {
	if segment != "" {
		b.extras = append(b.extras, segment)
	}
	return b
}

// Build assembles and returns the final system prompt string.
// Slots are joined with double newlines.
func (b *Builder) Build() string {
	var parts []string

	// Slot #1: SOUL.md — agent identity
	if b.ctxLoader != nil {
		if soul, err := b.ctxLoader.LoadSOUL(); err == nil && soul != "" {
			parts = append(parts, soul)
		}
	}

	// Slot #2: Memory snapshot from built-in provider
	if b.memMgr != nil {
		if bp := b.memMgr.GetProvider("builtin"); bp != nil {
			if block := bp.SystemPromptBlock(); block != "" {
				parts = append(parts, block)
			}
		}
	}

	// Slot #3: Project context
	if b.ctxLoader != nil {
		if proj, err := b.ctxLoader.LoadProjectContext(); err == nil && proj != "" {
			parts = append(parts, proj)
		}
	}

	// Slot #4: Platform-specific identity
	if b.platform != "" && b.platform != "cli" {
		parts = append(parts, platformIdentity(b.platform))
	}

	// Extra segments
	parts = append(parts, b.extras...)

	// Fallback identity if nothing loaded
	if len(parts) == 0 {
		parts = append(parts, "You are Hermes, a helpful AI assistant.")
	}

	return strings.Join(parts, "\n\n")
}

// platformIdentity returns a short identity string for non-CLI platforms.
func platformIdentity(platform string) string {
	switch platform {
	case "qq":
		return "You are responding via QQ messaging. Keep responses concise and friendly."
	case "telegram":
		return "You are responding via Telegram. Keep responses concise."
	case "discord":
		return "You are responding via Discord. Keep responses concise and friendly."
	case "cron":
		return "You are producing a scheduled AI report. Be thorough and well-formatted."
	default:
		return ""
	}
}

// BuildSimple is a convenience wrapper equivalent to NewBuilder(ctxLoader, memMgr).Build().
func BuildSimple(ctxLoader *hermescontext.Loader, memMgr *hermesmemory.MemoryManager) string {
	return NewBuilder(ctxLoader, memMgr).Build()
}

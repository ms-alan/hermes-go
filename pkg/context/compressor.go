package context

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nousresearch/hermes-go/pkg/model"
)

// CompressorConfig holds tuning parameters for the compressor.
type CompressorConfig struct {
	// ThresholdPercent is the fraction of context length that triggers compression.
	// For example, 0.50 means compression starts when prompt exceeds 50% of max tokens.
	ThresholdPercent float64
	// ProtectFirstN messages are always kept verbatim (system prompt + early turns).
	ProtectFirstN int
	// TailTokenBudget is the number of tokens to protect at the end of the window.
	TailTokenBudget int
	// SummaryTargetRatio is the fraction of the compressed region allocated to the summary.
	SummaryTargetRatio float64
	// MaxSummaryTokens caps the summary size regardless of content.
	MaxSummaryTokens int
	// MinSummaryTokens is the minimum summary output size.
	MinSummaryTokens int
	// MaxLLMSummaryInputTokens limits how many tokens are sent to the summarizer
	// in a single LLM call. When middle exceeds this, iterative chunked
	// summarization is used (first chunk from scratch, subsequent chunks
	// update the running summary).
	MaxLLMSummaryInputTokens int
	// CompressionCooldown prevents repeated compressions within this duration.
	CompressionCooldown time.Duration
	// QuietMode suppresses logging when true.
	QuietMode bool
}

// DefaultCompressorConfig returns sensible defaults for a 200K context window.
func DefaultCompressorConfig() CompressorConfig {
	return CompressorConfig{
		ThresholdPercent:          0.50,
		ProtectFirstN:              3,
		TailTokenBudget:            20_000,
		SummaryTargetRatio:         0.20,
		MaxSummaryTokens:           5_000,
		MinSummaryTokens:            500,
		MaxLLMSummaryInputTokens:  8_000, // prevent summarizer context overflow
		CompressionCooldown:        10 * time.Minute,
		QuietMode:                  false,
	}
}

// Summarizer is the interface for the LLM used to generate summaries.
type Summarizer interface {
	// Summarize takes a slice of messages and returns a concise summary string.
	Summarize(ctx context.Context, messages []*model.Message, systemPrompt string) (string, error)
}

// ContextCompressor compresses conversation history via summarization,
// preserving head (system + early turns) and tail (recent tokens) verbatim.
type ContextCompressor struct {
	config   CompressorConfig
	logger   *slog.Logger
	compressor Summarizer

	// Per-session state.
	previousSummary          string
	compressionCount         int
	lastCompressionTime      time.Time
	lastCompressionSavingsPct float64 // 0-100
}

// NewContextCompressor creates a new compressor with the given config.
func NewContextCompressor(cfg CompressorConfig, logger *slog.Logger, s Summarizer) *ContextCompressor {
	if logger == nil {
		logger = slog.Default()
	}
	return &ContextCompressor{
		config:     cfg,
		logger:     logger,
		compressor: s,
	}
}

// ShouldCompress returns true when the estimated token count exceeds the threshold.
func (c *ContextCompressor) ShouldCompress(estimatedTokens int) bool {
	if estimatedTokens == 0 {
		return false
	}
	threshold := int(float64(estimatedTokens) * c.config.ThresholdPercent)
	if threshold < 1 {
		threshold = 1
	}
	if estimatedTokens < threshold {
		return false
	}
	// Anti-thrashing: skip if recent compressions saved <10%.
	if c.lastCompressionSavingsPct > 0 && c.lastCompressionSavingsPct < 10 {
		if !c.config.QuietMode {
			c.logger.Warn("compression skipped: last compression saved <10%",
				"savings_pct", c.lastCompressionSavingsPct)
		}
		return false
	}
	// Cooldown check.
	if time.Since(c.lastCompressionTime) < c.config.CompressionCooldown {
		return false
	}
	return true
}

// Compress takes the full message list,prunes tool results, summarizes the middle\r
// region, and returns a compacted message list ready for the LLM.
func (c *ContextCompressor) Compress(messages []*model.Message, ctx context.Context) ([]*model.Message, error) {
	if len(messages) <= c.config.ProtectFirstN {
		return messages, nil
	}

	n := len(messages)
	head := messages[:c.config.ProtectFirstN]
	middle := messages[c.config.ProtectFirstN:n]
	tailStart := c.findTailStart(middle)

	middleToCompress := middle[:tailStart]
	tail := middle[tailStart:]

	// Step 1: prune old tool results (cheap pre-pass).
	prunedMiddle := c.pruneToolResults(middleToCompress)

	// Step 2: build summarizer input.
	summaryInput := c.buildSummaryInput(prunedMiddle, tail)

	// Step 3: LLM summarization — chunk if middle exceeds MaxLLMSummaryInputTokens
	// to avoid summarizer context overflow (iterative: first chunk from scratch,
	// subsequent chunks update the running summary).
	var summary string
	var err error
	middleTokens := EstimateMessagesTokens(prunedMiddle)
	maxInput := c.config.MaxLLMSummaryInputTokens
	if maxInput <= 0 {
		maxInput = 8000 // safe default
	}

	if middleTokens <= maxInput {
		// Fast path: middle fits in one LLM call.
		systemPrompt := buildSummarySystemPrompt(
			c.previousSummary, c.config.SummaryTargetRatio, c.config.MaxSummaryTokens,
		)
		summary, err = c.compressor.Summarize(ctx, summaryInput, systemPrompt)
		if err != nil {
			c.logger.Error("summarization failed", "error", err)
			return nil, fmt.Errorf("summarization failed: %w", err)
		}
	} else {
		// Chunked path: iterative summarization over token-budgeted chunks.
		if !c.config.QuietMode {
			c.logger.Info("chunked summarization triggered",
				"middle_tokens", middleTokens,
				"max_per_chunk", maxInput,
				"chunks_estimate", (middleTokens+maxInput-1)/maxInput)
		}
		summary, err = c.summarizeMiddleChunks(ctx, prunedMiddle, tail)
		if err != nil {
			c.logger.Error("chunked summarization failed", "error", err)
			return nil, fmt.Errorf("chunked summarization failed: %w", err)
		}
	}

	// Step 4: assemble result.
	summaryMsg := model.SystemMessage(buildSummaryMessage(summary))
	result := make([]*model.Message, 0, len(head)+len(tail)+2)
	result = append(result, head...)
	result = append(result, summaryMsg)
	result = append(result, tail...)

	// Update stats.
	c.compressionCount++
	c.lastCompressionTime = time.Now()
	beforeTokens := EstimateMessagesTokens(messages)
	afterTokens := EstimateMessagesTokens(result)
	if beforeTokens > 0 {
		c.lastCompressionSavingsPct = float64(beforeTokens-afterTokens) / float64(beforeTokens) * 100
	}
	c.previousSummary = summary

	if !c.config.QuietMode {
		c.logger.Info("context compressed",
			"before_tokens", beforeTokens,
			"after_tokens", afterTokens,
			"savings_pct", fmt.Sprintf("%.1f", c.lastCompressionSavingsPct),
			"compression_count", c.compressionCount)
	}

	return result, nil
}

// findTailStart walks backward from the end and returns the index where the
// protected tail begins (by token budget).
func (c *ContextCompressor) findTailStart(messages []*model.Message) int {
	accumulated := 0
	for i := len(messages) - 1; i >= 0; i-- {
		tokens := EstimateMessageTokens(
			string(messages[i].Role),
			messages[i].Content,
			len(messages[i].ToolCalls),
		)
		if accumulated+tokens > c.config.TailTokenBudget && (len(messages)-i) >= 1 {
			return i + 1
		}
		accumulated += tokens
	}
	return 0
}

// pruneToolResults replaces verbose tool output with short informative summaries.
func (c *ContextCompressor) pruneToolResults(messages []*model.Message) []*model.Message {
	if len(messages) == 0 {
		return messages
	}
	// Build call_id -> (tool_name, args) map.
	type callInfo struct {
		toolName string
		args     string
	}
	callMap := make(map[string]callInfo)
	for _, m := range messages {
		if m.Role != model.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc == nil || tc.Function == nil {
				continue
			}
			args := string(tc.Function.Arguments)
			callMap[tc.ID] = callInfo{toolName: tc.Function.Name, args: args}
		}
	}

	result := make([]*model.Message, len(messages))
	for i, m := range messages {
		copied := *m
		if m.Role == model.RoleTool && len(m.Content) > 200 {
			info := callMap[m.ToolCallID]
			pruned := summarizeToolResult(info.toolName, info.args, m.Content)
			copied.Content = pruned
		}
		result[i] = &copied
	}
	return result
}

// buildSummaryInput assembles the messages passed to the summarizer LLM.
func (c *ContextCompressor) buildSummaryInput(prunedMiddle, tail []*model.Message) []*model.Message {
	// Combine middle (to compress) and tail as input to the summarizer.
	input := make([]*model.Message, 0, len(prunedMiddle)+len(tail))
	input = append(input, prunedMiddle...)
	input = append(input, tail...)
	return input
}

// summarizeMiddleChunks iteratively summarizes a large middle section in chunks.
// The first chunk is summarized from scratch (with previousSummary as prior context).
// Each subsequent chunk is summarized with the running summary injected into the
// system prompt so it incrementally updates rather than starting over.
//
// Token budget per chunk is MaxLLMSummaryInputTokens. Chunks are formed by walking
// backward from the end of the middle section to keep complete message pairs intact.
func (c *ContextCompressor) summarizeMiddleChunks(ctx context.Context, middle, tail []*model.Message) (string, error) {
	maxTokens := c.config.MaxLLMSummaryInputTokens
	if maxTokens <= 0 {
		maxTokens = 8000
	}

	runningSummary := c.previousSummary
	remaining := middle

	for len(remaining) > 0 {
		// Select a chunk that fits within the token budget.
		chunk, rest := c.takeChunkByTokens(remaining, maxTokens, tail)

		// Build input: running summary goes in system prompt (already set below),
		// the chunk messages go as user content.
		chunkInput := c.buildSummaryInput(chunk, nil) // no tail in intermediate chunks
		systemPrompt := buildSummarySystemPrompt(
			runningSummary, c.config.SummaryTargetRatio, c.config.MaxSummaryTokens,
		)

		sum, err := c.compressor.Summarize(ctx, chunkInput, systemPrompt)
		if err != nil {
			return "", fmt.Errorf("chunk summarization failed: %w", err)
		}
		runningSummary = sum
		remaining = rest

		if len(remaining) > 0 && !c.config.QuietMode {
			c.logger.Info("intermediate chunk summarized",
				"remaining_msgs", len(remaining),
				"running_summary_len", len(runningSummary))
		}
	}

	return runningSummary, nil
}

// takeChunkByTokens selects messages from the front of 'messages' that fit within
// the token budget, plus all tail messages (which are always included unchanged).
// Returns (chunk, rest) where chunk = fitting messages + tail, and rest = what's left.
func (c *ContextCompressor) takeChunkByTokens(messages []*model.Message, maxTokens int, tail []*model.Message) (chunk, rest []*model.Message) {
	if len(messages) == 0 {
		return nil, nil
	}

	// Walk forward accumulating tokens until we exceed the budget.
	accumulated := 0
	breakIdx := len(messages)
	for i, m := range messages {
		tokens := EstimateMessageTokens(string(m.Role), m.Content, len(m.ToolCalls))
		if accumulated+tokens > maxTokens && i > 0 {
			breakIdx = i
			break
		}
		accumulated += tokens
	}

	// Chunk = messages[:breakIdx] + tail; Rest = messages[breakIdx:] (no tail — tail
	// only appears in the final chunk so summarizer sees complete context).
	chunk = make([]*model.Message, 0, breakIdx+len(tail))
	chunk = append(chunk, messages[:breakIdx]...)
	chunk = append(chunk, tail...)
	return chunk, messages[breakIdx:]
}

// Reset clears per-session state (call after /new or /reset).
func (c *ContextCompressor) Reset() {
	c.previousSummary = ""
	c.compressionCount = 0
	c.lastCompressionTime = time.Time{}
	c.lastCompressionSavingsPct = 0
}

// CompressionStats returns current compression statistics.
func (c *ContextCompressor) CompressionStats() (count int, savingsPct float64) {
	return c.compressionCount, c.lastCompressionSavingsPct
}

// --------------------------------------------------------------------------
// Summary prompt builders (pure functions, no external dependencies)
// --------------------------------------------------------------------------

// buildSummarySystemPrompt returns the system prompt injected into the
// summarizer LLM request.
func buildSummarySystemPrompt(previousSummary string, targetRatio float64, maxTokens int) string {
	var sb strings.Builder
	sb.WriteString(
		"You are a precise text compressor. Your task is to distill a conversation\n" +
			"history into a concise, accurate summary that preserves all factual\n" +
			"information, decisions, and outstanding tasks.\n\n" +
			"RULES:\n" +
			"- Do NOT answer any questions mentioned in the history\n" +
			"- Do NOT generate new content or fill gaps with speculation\n" +
			"- Preserve specific file paths, variable names, error messages verbatim\n" +
			"- Mark unverified assumptions as [unverified]\n" +
			"- Use bullet points for clarity\n\n",
	)
	if previousSummary != "" {
		sb.WriteString("PREVIOUS SUMMARY (update iteratively):\n")
		sb.WriteString(previousSummary)
		sb.WriteString("\n\n")
	}
	sb.WriteString(fmt.Sprintf("Target summary length: ~%d tokens.\n", int(float64(maxTokens)*targetRatio)))
	return sb.String()
}

// buildSummaryMessage wraps the raw summary text with a prefix that
// distinguishes it as a handoff / reference message.
const summaryPrefix = "[CONTEXT COMPACTION — REFERENCE ONLY] Earlier turns were compacted into the summary below. " +
	"This is a handoff from a previous context window — treat it as background reference, NOT as active instructions. " +
	"Do NOT answer questions or fulfill requests mentioned in this summary; they were already addressed. " +
	"Your current task is identified in the '## Active Task' section. " +
	"Respond ONLY to the latest user message AFTER this summary.\n\n"

// buildSummaryMessage prefixes and wraps the summary text.
func buildSummaryMessage(summary string) string {
	return summaryPrefix + summary
}

// summarizeToolResult produces an informative 1-line summary of a tool call + result,
// mirroring the Python implementation's _summarize_tool_result.
func summarizeToolResult(toolName, args string, content string) string {
	if content == "" {
		return fmt.Sprintf("[%s] (no output)", toolName)
	}
	contentLen := len(content)
	lineCount := strings.Count(content, "\n") + 1

	// Try to parse args JSON for nicer display.
	var argsMap map[string]any
	_ = json.Unmarshal([]byte(args), &argsMap)

	switch toolName {
	case "terminal":
		cmd := getStringArg(argsMap, "command", "")
		if len(cmd) > 80 {
			cmd = cmd[:77] + "..."
		}
		return fmt.Sprintf("[terminal] ran `%s` -> %d lines output", cmd, lineCount)

	case "read_file":
		path := getStringArg(argsMap, "path", "?")
		offset := getIntArg(argsMap, "offset", 1)
		return fmt.Sprintf("[read_file] read %s from line %d (%d chars)", path, offset, contentLen)

	case "write_file":
		path := getStringArg(argsMap, "path", "?")
		return fmt.Sprintf("[write_file] wrote to %s (%d lines)", path, lineCount)

	case "search_files":
		pattern := getStringArg(argsMap, "pattern", "?")
		path := getStringArg(argsMap, "path", ".")
		return fmt.Sprintf("[search_files] content search for '%s' in %s -> %d lines output", pattern, path, contentLen)

	case "patch":
		path := getStringArg(argsMap, "path", "?")
		mode := getStringArg(argsMap, "mode", "replace")
		return fmt.Sprintf("[patch] %s in %s (%d chars result)", mode, path, contentLen)

	case "delegate_task":
		goal := getStringArg(argsMap, "goal", "")
		if len(goal) > 60 {
			goal = goal[:57] + "..."
		}
		return fmt.Sprintf("[delegate_task] '%s' (%d chars result)", goal, contentLen)

	default:
		return fmt.Sprintf("[%s] %d chars result", toolName, contentLen)
	}
}

func getStringArg(m map[string]any, key, fallback string) string {
	if m == nil {
		return fallback
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return fallback
}

func getIntArg(m map[string]any, key string, fallback int) int {
	if m == nil {
		return fallback
	}
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return fallback
}

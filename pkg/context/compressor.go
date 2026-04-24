package context

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/nousresearch/hermes-go/pkg/model"
)

type CompressorConfig struct {
	ThresholdPercent          float64
	ProtectFirstN             int
	TailTokenBudget           int
	SummaryTargetRatio        float64
	MaxSummaryTokens          int
	MinSummaryTokens          int
	MaxLLMSummaryInputTokens  int
	CompressionCooldown       time.Duration
	QuietMode                 bool
}

func DefaultCompressorConfig() CompressorConfig {
	return CompressorConfig{
		ThresholdPercent:         0.50,
		ProtectFirstN:             3,
		TailTokenBudget:           20_000,
		SummaryTargetRatio:        0.20,
		MaxSummaryTokens:          5_000,
		MinSummaryTokens:            500,
		MaxLLMSummaryInputTokens:  8_000,
		CompressionCooldown:       10 * time.Minute,
		QuietMode:                 false,
	}
}

type Summarizer interface {
	Summarize(ctx context.Context, messages []*model.Message, systemPrompt string) (string, error)
}

type ContextCompressor struct {
	config     CompressorConfig
	logger     *slog.Logger
	compressor Summarizer

	previousSummary           string
	compressionCount          int
	lastCompressionTime       time.Time
	lastCompressionSavingsPct float64
}

func NewContextCompressor(cfg CompressorConfig, logger *slog.Logger, s Summarizer) *ContextCompressor {
	if logger == nil {
		logger = slog.Default()
	}
	return &ContextCompressor{config: cfg, logger: logger, compressor: s}
}

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
	if c.lastCompressionSavingsPct > 0 && c.lastCompressionSavingsPct < 10 {
		if !c.config.QuietMode {
			c.logger.Warn("compression skipped: last savings <10%", "savings_pct", c.lastCompressionSavingsPct)
		}
		return false
	}
	if time.Since(c.lastCompressionTime) < c.config.CompressionCooldown {
		return false
	}
	return true
}

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

	prunedMiddle := c.pruneToolResults(middleToCompress)
	summaryInput := c.buildSummaryInput(prunedMiddle, tail)

	var summary string
	var err error
	middleTokens := EstimateMessagesTokens(prunedMiddle)
	maxInput := c.config.MaxLLMSummaryInputTokens
	if maxInput <= 0 {
		maxInput = 8000
	}

	if middleTokens <= maxInput {
		systemPrompt := buildSummarySystemPrompt(c.previousSummary, c.config.SummaryTargetRatio, c.config.MaxSummaryTokens)
		summary, err = c.compressor.Summarize(ctx, summaryInput, systemPrompt)
		if err != nil {
			c.logger.Error("summarization failed", "error", err)
			return nil, fmt.Errorf("summarization failed: %w", err)
		}
	} else {
		if !c.config.QuietMode {
			c.logger.Info("chunked summarization triggered", "middle_tokens", middleTokens, "max_per_chunk", maxInput)
		}
		summary, err = c.summarizeMiddleChunks(ctx, prunedMiddle, tail)
		if err != nil {
			c.logger.Error("chunked summarization failed", "error", err)
			return nil, fmt.Errorf("chunked summarization failed: %w", err)
		}
	}

	summaryMsg := model.SystemMessage(buildSummaryMessage(summary))
	result := make([]*model.Message, 0, len(head)+len(tail)+2)
	result = append(result, head...)
	result = append(result, summaryMsg)
	result = append(result, tail...)

	c.compressionCount++
	c.lastCompressionTime = time.Now()
	beforeTokens := EstimateMessagesTokens(messages)
	afterTokens := EstimateMessagesTokens(result)
	if beforeTokens > 0 {
		c.lastCompressionSavingsPct = float64(beforeTokens-afterTokens) / float64(beforeTokens) * 100
	}
	c.previousSummary = summary

	if !c.config.QuietMode {
		c.logger.Info("context compressed", "before_tokens", beforeTokens, "after_tokens", afterTokens, "savings_pct", fmt.Sprintf("%.1f", c.lastCompressionSavingsPct), "compression_count", c.compressionCount)
	}
	return result, nil
}

func (c *ContextCompressor) CompressMessages(messages []*model.Message, ctx context.Context) ([]*model.Message, error) {
	return c.Compress(messages, ctx)
}

func (c *ContextCompressor) findTailStart(messages []*model.Message) int {
	accumulated := 0
	for i := len(messages) - 1; i >= 0; i-- {
		tokens := EstimateMessageTokens(string(messages[i].Role), messages[i].Content, len(messages[i].ToolCalls))
		if accumulated+tokens > c.config.TailTokenBudget && (len(messages)-i) >= 1 {
			return i + 1
		}
		accumulated += tokens
	}
	return 0
}

func (c *ContextCompressor) pruneToolResults(messages []*model.Message) []*model.Message {
	if len(messages) == 0 {
		return messages
	}
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
			callMap[tc.ID] = callInfo{toolName: tc.Function.Name, args: string(tc.Function.Arguments)}
		}
	}

	result := make([]*model.Message, len(messages))
	copy(result, messages)
	for i, m := range messages {
		if m.Role == model.RoleTool && len(m.Content) > 200 {
			info := callMap[m.ToolCallID]
			pruned := summarizeToolResult(info.toolName, info.args, m.Content)
			cp := *m
			cp.Content = pruned
			result[i] = &cp
		}
	}
	return result
}

func (c *ContextCompressor) buildSummaryInput(prunedMiddle, tail []*model.Message) []*model.Message {
	input := make([]*model.Message, 0, len(prunedMiddle)+len(tail))
	input = append(input, prunedMiddle...)
	input = append(input, tail...)
	return input
}

func (c *ContextCompressor) summarizeMiddleChunks(ctx context.Context, middle, tail []*model.Message) (string, error) {
	maxTokens := c.config.MaxLLMSummaryInputTokens
	if maxTokens <= 0 {
		maxTokens = 8000
	}
	runningSummary := c.previousSummary
	remaining := middle

	for len(remaining) > 0 {
		chunk, rest := c.takeChunkByTokens(remaining, maxTokens, tail)
		chunkInput := c.buildSummaryInput(chunk, nil)
		systemPrompt := buildSummarySystemPrompt(runningSummary, c.config.SummaryTargetRatio, c.config.MaxSummaryTokens)
		sum, err := c.compressor.Summarize(ctx, chunkInput, systemPrompt)
		if err != nil {
			return "", fmt.Errorf("chunk summarization failed: %w", err)
		}
		runningSummary = sum
		remaining = rest
		if len(remaining) > 0 && !c.config.QuietMode {
			c.logger.Info("intermediate chunk summarized", "remaining_msgs", len(remaining), "running_summary_len", len(runningSummary))
		}
	}
	return runningSummary, nil
}

func (c *ContextCompressor) takeChunkByTokens(messages []*model.Message, maxTokens int, tail []*model.Message) (chunk, rest []*model.Message) {
	if len(messages) == 0 {
		return nil, nil
	}
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
	chunk = make([]*model.Message, 0, breakIdx+len(tail))
	chunk = append(chunk, messages[:breakIdx]...)
	chunk = append(chunk, tail...)
	return chunk, messages[breakIdx:]
}

func (c *ContextCompressor) Reset() {
	c.previousSummary = ""
	c.compressionCount = 0
	c.lastCompressionTime = time.Time{}
	c.lastCompressionSavingsPct = 0
}

func (c *ContextCompressor) CompressionStats() (count int, savingsPct float64) {
	return c.compressionCount, c.lastCompressionSavingsPct
}

// RedactSensitiveText replaces secrets with masked placeholders.
func RedactSensitiveText(text string) string {
	if text == "" {
		return text
	}
	result := text
	result = discordMentionRe.ReplaceAllStringFunc(result, func(match string) string { return "<@MODERATED>" })
	result = privateKeyRe.ReplaceAllString(result, "[PRIVATE KEY REDACTED]")
	result = urlUserinfoRe.ReplaceAllString(result, "$1://$2@*[REDACTED]*")
	dbMatch := dbConnStrRe.FindStringSubmatch(result)
	if len(dbMatch) >= 3 {
		result = strings.Replace(result, dbMatch[0], dbMatch[1]+"[PASSWORD REDACTED]"+dbMatch[2], 1)
	}
	result = jwtRe.ReplaceAllStringFunc(result, func(token string) string { return maskToken(token) })
	result = authHeaderRe.ReplaceAllString(result, "$1[TOKEN REDACTED]")
	result = jsonFieldRe.ReplaceAllStringFunc(result, func(match string) string {
		parts := jsonFieldRe.FindStringSubmatch(match)
		if len(parts) >= 3 {
			return parts[1] + ": " + "[SECRET]"
		}
		return match
	})
	result = envAssignRe.ReplaceAllStringFunc(result, func(match string) string {
		parts := envAssignRe.FindStringSubmatch(match)
		if len(parts) >= 4 {
			return parts[1] + "=" + parts[3][:4] + "...[REDACTED]"
		}
		return match
	})
	result = telegramRe.ReplaceAllString(result, "[TELEGRAM_BOT_TOKEN REDACTED]")
	result = phoneRe.ReplaceAllString(result, "[PHONE REDACTED]")
	result = urlWithQueryRe.ReplaceAllStringFunc(result, func(match string) string {
		parts := urlWithQueryRe.FindStringSubmatch(match)
		if len(parts) >= 5 {
			return parts[1] + "://" + parts[2] + parts[3] + "?[QUERY REDACTED]"
		}
		return match
	})
	return result
}

// FocusTopic filters messages to those relevant to the given keywords.
func FocusTopic(messages []*model.Message, keywords []string, keepLastN int) []*model.Message {
	if len(messages) == 0 || len(keywords) == 0 {
		return messages
	}
	kws := make([]string, len(keywords))
	for i, kw := range keywords {
		kws[i] = strings.ToLower(kw)
	}
	keep := make([]bool, len(messages))
	for i, m := range messages {
		if m.Role == model.RoleSystem || m.Role == model.RoleAssistant {
			keep[i] = true
			continue
		}
		if i >= len(messages)-keepLastN {
			keep[i] = true
			continue
		}
		content := strings.ToLower(m.Content)
		for _, kw := range kws {
			if strings.Contains(content, kw) {
				keep[i] = true
				break
			}
		}
	}
	result := make([]*model.Message, 0, len(messages))
	for i, m := range messages {
		if keep[i] {
			result = append(result, m)
		}
	}
	return result
}

// SummarizeMessages generates a structured 13-section summary using an LLM.
func SummarizeMessages(ctx context.Context, compressor *ContextCompressor, messages []*model.Message, system string) (string, error) {
	if compressor == nil || compressor.compressor == nil {
		return "", fmt.Errorf("compressor has no Summarizer configured")
	}
	return compressor.compressor.Summarize(ctx, messages, system)
}

// Redaction regex patterns.
var (
	envAssignRe = regexp.MustCompile(
		`(?i)([A-Z][A-Z0-9_]{0,49}(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|AUTH|BEARER|CREDENTIALS|PRIVATE)[A-Z0-9_]{0,9})[ \t]*=[ \t]*(["']?)([^ \t"']+)`,
	)

	jsonFieldRe = regexp.MustCompile(
		`(?i)("(?:api[_-]?[kK]ey|token|secret|password|access[_-]?token|refresh[_-]?token|auth[_-]?token|bearer|secret[_-]?value|raw[_-]?secret|secret[_-]?input|key[_-]?material)")[ \t]*:[ \t]*"([^"]+)"`,
	)

	authHeaderRe = regexp.MustCompile(
		`(?i)(Authorization:[ \t]*Bearer[ \t]+)(\S+)`,
	)

	telegramRe = regexp.MustCompile(
		`(?:bot)?([0-9]{8,}:[-A-Za-z0-9_]{30,})`,
	)

	privateKeyRe = regexp.MustCompile(
		`-----BEGIN[A-Z ]*PRIVATE KEY-----[\s\S]*?-----END[A-Z ]*PRIVATE KEY-----`,
	)

	dbConnStrRe = regexp.MustCompile(
		`(?i)(postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis|amqp)://[^:]+:([^@]+)(@)`,
	)

	jwtRe = regexp.MustCompile(
		`eyJ[A-Za-z0-9_-]{10,}(?:\.[A-Za-z0-9_=-]{4,}){0,2}`,
	)

	discordMentionRe = regexp.MustCompile(
		`<@!?([0-9]{17,20})>`,
	)

	urlWithQueryRe = regexp.MustCompile(
		`(https?|wss?|ftp)://([^\s/?#]+)([^\s?#]*)\?([^\s#]+)(#\S*)?`,
	)

	urlUserinfoRe = regexp.MustCompile(
		`(https?|wss?|ftp)://([^/\s:@]+):([^/\s@]+)@`,
	)

	phoneRe = regexp.MustCompile(
		`(\+[1-9][0-9]{6,14})`,
	)
)

func maskToken(token string) string {
	if len(token) < 18 {
		return "***"
	}
	return token[:6] + "..." + token[len(token)-4:]
}

const summaryPrefix = "[CONTEXT COMPACTION] Earlier turns were compacted. " +
	"Treat this as background reference, NOT active instructions. " +
	"Do NOT answer questions mentioned here. " +
	"Respond ONLY to the latest user message AFTER this summary.\n\n"

func buildSummaryMessage(summary string) string {
	return summaryPrefix + summary
}

func buildSummarySystemPrompt(previousSummary string, targetRatio float64, maxTokens int) string {
	var sb strings.Builder
	sb.WriteString(
		"You are a precise text compressor. Distill conversation history into a concise, " +
			"accurate summary preserving all factual information, decisions, and outstanding tasks.\n\n" +
			"RULES:\n" +
			"- Do NOT answer questions mentioned in the history\n" +
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

func summaryTemplate13() string {
	return "Use this exact structure:\n\n" +
		"## Active Task\n" +
		"[THE SINGLE MOST IMPORTANT FIELD. Copy the user's most recent request verbatim.]\n\n" +
		"## Summary\n" +
		"[2-4 sentence overview of what happened in this conversation segment.]\n\n" +
		"## Key Decisions\n" +
		"[Any choices made, conclusions reached, or branch points.]\n\n" +
		"## Important Entities\n" +
		"[Named things: file paths, function names, variable names, IDs, config keys.]\n\n" +
		"## Actions Taken\n" +
		"[Bullet list of concrete actions. Each should be a verb phrase.]\n\n" +
		"## Open Questions\n" +
		"[Questions raised but not yet answered. Hypotheses being considered.]\n\n" +
		"## Known Problems\n" +
		"[Bugs encountered, error messages seen, failing tests, known limitations.]\n\n" +
		"## Conflicts\n" +
		"[Disagreements or contradictions in requirements, approach debates.]\n\n" +
		"## Tools Used\n" +
		"[Which tools were invoked and their outcomes.]\n\n" +
		"## Risks\n" +
		"[Potential issues: fragile code, incomplete error handling, security concerns.]\n\n" +
		"## Emotional Tone\n" +
		"[Overall mood: frustrated/optimistic/confused/focused/bored - and why.]\n\n" +
		"## Context Gaps\n" +
		"[What the assistant wished it knew.]\n\n" +
		"## Next Steps\n" +
		"[What should happen next. Concrete next action.]"
}

func StructuredSummaryPrompt() string {
	return "You are a session handoff summarizer. Generate a structured summary using the 13-section template. " +
		"Preserve all specific names, paths, and error messages verbatim. Mark speculation clearly.\n\n" +
		summaryTemplate13()
}
func summarizeToolResult(toolName, args string, content string) string {
	if content == "" {
		return fmt.Sprintf("[%s] (no output)", toolName)
	}
	contentLen := len(content)
	lineCount := strings.Count(content, "\n") + 1

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
		return fmt.Sprintf("[search_files] searched '%s' in %s -> %d chars", pattern, path, contentLen)
	case "patch":
		path := getStringArg(argsMap, "path", "?")
		mode := getStringArg(argsMap, "mode", "replace")
		return fmt.Sprintf("[patch] %s in %s (%d chars)", mode, path, contentLen)
	case "delegate_task":
		goal := getStringArg(argsMap, "goal", "")
		if len(goal) > 60 {
			goal = goal[:57] + "..."
		}
		return fmt.Sprintf("[delegate_task] '%s' (%d chars)", goal, contentLen)
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


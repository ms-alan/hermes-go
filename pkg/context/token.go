package context

import (
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/pkoukk/tiktoken-go"
)

var (
	// cl100k_base is the encoding used by GPT-4, GPT-3.5-turbo, and many others.
	// Lazily initialized on first use; thread-safe.
	cl100kBase    *tiktoken.Tiktoken
	cl100kBaseErr error
	cl100kOnce    sync.Once

	// Chinese/Japanese/Korean characters count as ~2 tokens on average.
	cjkRegex    = regexp.MustCompile(`[\x{4E00}-\x{9FFF}\x{3400}-\x{4DBF}\x{3000}-\x{303F}\x{3040}-\x{309F}\x{30A0}-\x{30FF}\x{31F0}-\x{31FF}]`)
	multiSpace   = regexp.MustCompile(`\s+`)
	toolCallLine = regexp.MustCompile(`(?m)^ToolCall\([^)]*\)\s*$`)
)

// getCl100kEncoder returns the cl100k_base encoder, initializing it on first call.
// Thread-safe. Returns nil if initialization failed (e.g., no network on first call).
func getCl100kEncoder() (*tiktoken.Tiktoken, error) {
	cl100kOnce.Do(func() {
		cl100kBase, cl100kBaseErr = tiktoken.GetEncoding("cl100k_base")
	})
	return cl100kBase, cl100kBaseErr
}

// EstimateTokens estimates the number of tokens for a string using tiktoken cl100k_base.
// Falls back to the character heuristic if tiktoken initialization failed
// (e.g., network error on first call in offline environments).
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	enc, err := getCl100kEncoder()
	if err == nil && enc != nil {
		return len(enc.Encode(text, nil, nil))
	}
	// Fallback: rough character-based heuristic.
	return estimateTokensHeuristic(text)
}

// estimateTokensHeuristic is the original character-based estimator.
// Used as fallback when tiktoken is unavailable (offline environment).
func estimateTokensHeuristic(text string) int {
	cjkCount := int64(len(cjkRegex.FindAllString(text, -1)))
	asciiText := cjkRegex.ReplaceAllString(text, "")
	asciiTokens := int64(len(asciiText)) / 4
	cjkTokens := int64(float64(cjkCount) * 1.5)
	return int(asciiTokens + cjkTokens) + 10
}

// EstimateMessageTokens estimates token count for a single message,
// including tool_calls overhead.
// Uses tiktoken when available.
func EstimateMessageTokens(role, content string, toolCalls int) int {
	if content == "" && toolCalls == 0 {
		return 4 // bare message overhead
	}
	tokens := EstimateTokens(content)
	// Tool calls add ~15 tokens each plus argument length.
	tokens += toolCalls * 15
	// Role and delimiter overhead.
	tokens += 4
	return tokens
}

// EstimateMessagesTokens is in manager.go (shares same package, tiktoken-aware).

// CountTokensForModel uses the correct tiktoken encoding for the given model.
// Falls back to cl100k_base if the model is not recognized.
func CountTokensForModel(modelName, text string) int {
	if text == "" {
		return 0
	}
	enc, err := tiktoken.EncodingForModel(modelName)
	if err != nil || enc == nil {
		return EstimateTokens(text)
	}
	return len(enc.Encode(text, nil, nil))
}

// MaxTokensForModel returns a safe "max response tokens" default for a given
// model context length, reserving ~10% for the response.
func MaxTokensForModel(contextLength int) int {
	return contextLength / 10
}

// TokenBudget holds token allocation for a conversation window.
type TokenBudget struct {
	MaxTokens      int
	UsedTokens     int
	ReservedTokens int // tokens reserved for system prompt / fixed overhead
}

// Remaining returns the number of tokens still available for messages.
func (b TokenBudget) Remaining() int {
	return b.MaxTokens - b.UsedTokens - b.ReservedTokens
}

// ApproachingLimit returns true when >80% of available budget is consumed.
func (b TokenBudget) ApproachingLimit() bool {
	available := b.MaxTokens - b.ReservedTokens
	if available <= 0 {
		return true
	}
	return float64(b.UsedTokens)/float64(available) > 0.80
}

// FormatTokenCount returns a human-readable string for a token count.
func FormatTokenCount(tokens int) string {
	if tokens >= 1000 {
		return strings.ToLower(strings.ReplaceAll(
			strings.TrimSpace(strings.TrimLeft(formatFloat(float64(tokens)/1000, 1), "0")), ".", ",")) + "K"
	}
	return itoa(tokens)
}

// formatFloat formats a float without importing strconv.
func formatFloat(val float64, prec int) string {
	scale := intPow(10, prec)
	whole := int(val)
	frac := abs(int(val*float64(scale) - float64(whole)*float64(scale)))
	sign := ""
	if whole < 0 {
		sign = "-"
		whole = -whole
	}
	return sign + itoa(whole) + "." + itoa(frac)
}

func intPow(base, exp int) int {
	result := 1
	for i := 0; i < exp; i++ {
		result *= base
	}
	return result
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// NormalizeWhitespace collapses multiple whitespace chars, stripping leading/trailing.
func NormalizeWhitespace(text string) string {
	text = multiSpace.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

// IsMostlyCJK returns true if more than 40% of the text is CJK characters.
func IsMostlyCJK(text string) bool {
	if text == "" {
		return false
	}
	cjkCount := len(cjkRegex.FindAllString(text, -1))
	total := 0
	for _, r := range text {
		if !unicode.IsSpace(r) {
			total++
		}
	}
	if total == 0 {
		return false
	}
	return float64(cjkCount)/float64(total) > 0.4
}

// itoa is a simple integer-to-string.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var sb strings.Builder
	sb.Grow(12)
	for n > 0 {
		sb.WriteByte(byte('0' + n%10))
		n /= 10
	}
	bytes := []byte(sb.String())
	for i, j := 0, len(bytes)-1; i < j; i, j = i+1, j-1 {
		bytes[i], bytes[j] = bytes[j], bytes[i]
	}
	if negative {
		return "-" + string(bytes)
	}
	return string(bytes)
}

// TokenizerEngineInfo returns the current token counting engine and encoding.
func TokenizerEngineInfo() (engine, encoding string, err error) {
	enc, err := getCl100kEncoder()
	if err != nil || enc == nil {
		return "heuristic", "n/a", err
	}
	return "tiktoken", "cl100k_base", nil
}

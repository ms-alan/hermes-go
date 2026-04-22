package agent

import (
	"encoding/json"
	"strings"
)

// ThinkingExtractor extracts thinking/reasoning content from model responses.
type ThinkingExtractor struct {
	// ThinkingKey is the content block key that contains thinking (e.g., "thinking", "reasoning").
	ThinkingKey string
}

// NewThinkingExtractor creates a ThinkingExtractor with the default "thinking" key.
func NewThinkingExtractor() *ThinkingExtractor {
	return &ThinkingExtractor{ThinkingKey: "thinking"}
}

// Extract attempts to pull reasoning/thinking content from a content block list.
// It searches for a block with Type matching ThinkingKey and returns its Text.
func (te *ThinkingExtractor) Extract(blocks []ContentBlock) (string, bool) {
	key := strings.ToLower(te.ThinkingKey)
	for _, block := range blocks {
		if strings.ToLower(block.Type) == key && block.Text != "" {
			return block.Text, true
		}
	}
	return "", false
}

// ExtractFromMessage extracts thinking from a model message's content blocks.
func (te *ThinkingExtractor) ExtractFromMessage(contentJSON string) (string, bool) {
	if contentJSON == "" {
		return "", false
	}
	// Try to parse as array of content blocks
	var blocks []ContentBlock
	if err := json.Unmarshal([]byte(contentJSON), &blocks); err != nil {
		return "", false
	}
	return te.Extract(blocks)
}

// ContentBlock represents a block of content from a model response.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ParseContentBlocks parses a JSON content array into ContentBlocks.
func ParseContentBlocks(data []byte) ([]ContentBlock, error) {
	var blocks []ContentBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

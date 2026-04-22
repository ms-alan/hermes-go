package model

import "encoding/json"

// Role represents the sender of a message in a conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Content represents the content of a message.
type Content struct {
	Text string `json:"text,omitempty"`
}

// Message represents a single message in a conversation.
type Message struct {
	Role           Role          `json:"role"`
	Content        string        `json:"content"`
	Name           string        `json:"name,omitempty"`
	ToolCallID     string        `json:"tool_call_id,omitempty"`
	ToolCalls      []*ToolCall   `json:"tool_calls,omitempty"`
	ToolPlan       string        `json:"tool_plan,omitempty"` // reasoning/thinking content
	StopReason     string        `json:"stop_reason,omitempty"`
}

// ToolCall represents a tool call from the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function *FunctionCall `json:"function"`
}

// FunctionCall represents the function portion of a tool call.
type FunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"` // JSON string of the arguments
}

// AssistantMessage converts a Message to an assistant role message.
func (m *Message) AssistantMessage() *Message {
	return &Message{Role: RoleAssistant, Content: m.Content, ToolCalls: m.ToolCalls, ToolPlan: m.ToolPlan, StopReason: m.StopReason}
}

// ToolMessage creates a tool result message.
func ToolMessage(callID, content string) *Message {
	return &Message{Role: RoleTool, ToolCallID: callID, Content: content}
}

// SystemMessage creates a system message.
func SystemMessage(content string) *Message {
	return &Message{Role: RoleSystem, Content: content}
}

// UserMessage creates a user message.
func UserMessage(content string) *Message {
	return &Message{Role: RoleUser, Content: content}
}

// ParseToolArguments unmarshals the arguments from a ToolCall into a map.
func (tc *ToolCall) ParseArguments(args interface{}) error {
	return json.Unmarshal(tc.Function.Arguments, args)
}

// Clone returns a deep copy of the ToolCall.
func (tc *ToolCall) Clone() *ToolCall {
	if tc == nil {
		return nil
	}
	var argsCopy json.RawMessage
	if tc.Function != nil {
		argsCopy = make(json.RawMessage, len(tc.Function.Arguments))
		copy(argsCopy, tc.Function.Arguments)
	}
	return &ToolCall{
		ID:       tc.ID,
		Type:     tc.Type,
		Function: &FunctionCall{
			Name:      tc.Function.Name,
			Arguments: argsCopy,
		},
	}
}

// Clone returns a deep copy of the Message.
func (m *Message) Clone() *Message {
	if m == nil {
		return nil
	}
	cloned := &Message{
		Role:       m.Role,
		Content:    m.Content,
		Name:       m.Name,
		ToolCallID: m.ToolCallID,
		ToolPlan:   m.ToolPlan,
		StopReason: m.StopReason,
	}
	if m.ToolCalls != nil {
		cloned.ToolCalls = make([]*ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			cloned.ToolCalls[i] = tc.Clone()
		}
	}
	return cloned
}

// GetArguments returns the raw arguments as a JSON string.
func (tc *ToolCall) GetArguments() string {
	return string(tc.Function.Arguments)
}

package model

// Tool represents a tool that the model can call.
type Tool struct {
	Type     string       `json:"type"`
	Function *FunctionDef `json:"function"`
}

// FunctionDef defines a callable function tool.
type FunctionDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"` // JSON schema object
}

// ToolChoice controls which tool the model uses.
type ToolChoice struct {
	Type     string `json:"type"` // "function" or "none"
	Function *struct {
		Name string `json:"name"`
	} `json:"function,omitempty"`
}

// ToolCallRequest is the input to a tool call handler.
type ToolCallRequest struct {
	Name      string
	Arguments map[string]any
	ID        string
}

// ToolResult is the output from a tool call handler.
type ToolResult struct {
	Content string
	Error   error
	IsError bool
}

// NewToolResult creates a successful tool result.
func NewToolResult(content string) *ToolResult {
	return &ToolResult{Content: content}
}

// NewToolError creates an error tool result.
func NewToolError(err error) *ToolResult {
	return &ToolResult{Content: err.Error(), Error: err, IsError: true}
}

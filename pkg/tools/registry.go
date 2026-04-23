// Package tools provides a central registry for all hermes-go tools.
//
// Each builtin tool file calls registry.Register at package init time to
// declare its schema, handler, toolset membership, and availability check.
// The registry is queried by the agent loop instead of maintaining parallel
// data structures.
//
// Import chain (circular-import safe):
//
//	tools/registry.go  (no imports from other tool files)
//	       ↑
//	tools/*.go  (import from tools.registry at package init)
//	       ↑
//	agent or main packages
package tools

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
)

// --------------------------------------------------------------------------.
// ToolCall represents an incoming tool invocation from the model.
type ToolCall struct {
	Name      string
	Arguments map[string]any // raw JSON-parsed arguments
}

// ToolResult represents the outcome of a tool execution.
type ToolResult struct {
	Name    string `json:"name"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
	Success bool   `json:"success"`
}

// toolSchema mirrors the OpenAI function-call schema format.
type toolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]any          `json:"parameters,omitempty"`
}

// ToolEntry holds metadata for a single registered tool.
type ToolEntry struct {
	Name        string
	Toolset     string
	Schema      toolSchema
	Handler     func(args map[string]any) string
	CheckFn     func() bool
	RequiresEnv []string
	IsAsync     bool
	Description string
	Emoji       string
}

// ToolRegistry is the singleton registry that collects tool schemas and handlers.
type ToolRegistry struct {
	mu             sync.RWMutex
	tools          map[string]*ToolEntry
	toolsetChecks  map[string]func() bool
}

// NewRegistry returns a fresh registry. In normal use a package-level singleton
// "Registry" is exported instead.
func NewRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:         make(map[string]*ToolEntry),
		toolsetChecks: make(map[string]func() bool),
	}
}

// Register adds a tool to the registry. It is called at package init time by
// each tool file. Register will panic if a tool with the same name is already
// registered from a different toolset (MCP-to-MCP overwrites are allowed).
func (r *ToolRegistry) Register(
	name, toolset string,
	schema map[string]any,
	handler func(args map[string]any) string,
	checkFn func() bool,
	requiresEnv []string,
	isAsync bool,
	description, emoji string,
) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry := &ToolEntry{
		Name:        name,
		Toolset:     toolset,
		Schema:      schemaToToolSchema(schema, name),
		Handler:     handler,
		CheckFn:     checkFn,
		RequiresEnv: requiresEnv,
		IsAsync:     isAsync,
		Description: description,
		Emoji:       emoji,
	}

	if existing, ok := r.tools[name]; ok && existing.Toolset != toolset {
		// Allow MCP-to-MCP overwrites; reject built-in shadowing.
		bothMCP := isMCP(toolset) && isMCP(existing.Toolset)
		if !bothMCP {
			log.Printf("WARN: tool registration REJECTED: %q (toolset %q) would shadow existing tool from toolset %q — skipping",
				name, toolset, existing.Toolset)
			return
		}
	}

	r.tools[name] = entry
	if checkFn != nil {
		if _, exists := r.toolsetChecks[toolset]; !exists {
			r.toolsetChecks[toolset] = checkFn
		}
	}
}

// isMCP returns true if the toolset name starts with "mcp-".
func isMCP(toolset string) bool {
	return len(toolset) >= 4 && toolset[:4] == "mcp-"
}

// schemaToToolSchema converts a raw map into the internal toolSchema struct,
// ensuring the "name" field is always populated.
func schemaToToolSchema(schema map[string]any, name string) toolSchema {
	s := toolSchema{Name: name}
	if desc, ok := schema["description"].(string); ok {
		s.Description = desc
	}
	if params, ok := schema["parameters"].(map[string]any); ok {
		s.Parameters = params
	}
	return s
}

// GetEntry returns the tool entry for the given name, or nil.
func (r *ToolRegistry) GetEntry(name string) *ToolEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

// Call looks up the tool by name, runs its handler, and returns a ToolResult.
// If the tool is unknown, returns an error result.
func (r *ToolRegistry) Call(name string, args map[string]any) ToolResult {
	entry := r.GetEntry(name)
	if entry == nil {
		return ToolResult{Name: name, Error: fmt.Sprintf("unknown tool: %s", name), Success: false}
	}

	if entry.CheckFn != nil && !entry.CheckFn() {
		return ToolResult{Name: name, Error: "tool unavailable (check failed)", Success: false}
	}

	var output string
	func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		output = entry.Handler(args)
		return nil
	}()

	if output == "" {
		output = "{}"
	}

	// Attempt to detect whether the output is a JSON error object.
	var errCheck map[string]any
	if json.Unmarshal([]byte(output), &errCheck) == nil {
		if _, hasError := errCheck["error"]; hasError {
			return ToolResult{Name: name, Output: output, Error: errCheck["error"].(string), Success: false}
		}
	}

	return ToolResult{Name: name, Output: output, Success: true}
}

// List returns the names of all registered tools.
func (r *ToolRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// ListEntries returns a snapshot of all tool entries. Thread-safe.
func (r *ToolRegistry) ListEntries() []*ToolEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entries := make([]*ToolEntry, 0, len(r.tools))
	for _, e := range r.tools {
		entries = append(entries, e)
	}
	return entries
}

// GetDefinitions returns OpenAI-format tool definitions for the given tool names.
// Only tools whose CheckFn returns true (or have no CheckFn) are included.
func (r *ToolRegistry) GetDefinitions(names []string) []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]map[string]any, 0, len(names))
	for _, name := range names {
		entry, ok := r.tools[name]
		if !ok {
			continue
		}
		if entry.CheckFn != nil && !entry.CheckFn() {
			continue
		}
		schema := entry.Schema
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        schema.Name,
				"description": schema.Description,
				"parameters":  schema.Parameters,
			},
		})
	}
	return result
}

// GetToolsetForTool returns the toolset a tool belongs to, or empty string.
func (r *ToolRegistry) GetToolsetForTool(name string) string {
	entry := r.GetEntry(name)
	if entry == nil {
		return ""
	}
	return entry.Toolset
}

// GetAllToolNames returns a sorted list of all registered tool names.
func (r *ToolRegistry) GetAllToolNames() []string {
	return r.List()
}

// GetToolNamesForToolset returns sorted tool names registered under a given toolset.
func (r *ToolRegistry) GetToolNamesForToolset(toolset string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for _, entry := range r.tools {
		if entry.Toolset == toolset {
			names = append(names, entry.Name)
		}
	}
	return names
}

// IsToolsetAvailable checks whether a toolset's requirements are met.
func (r *ToolRegistry) IsToolsetAvailable(toolset string) bool {
	r.mu.RLock()
	check := r.toolsetChecks[toolset]
	r.mu.RUnlock()
	if check == nil {
		return true
	}
	defer func() {
		if recover() != nil {
			// Check function panicked; treat as unavailable.
		}
	}()
	return check()
}

// Deregister removes a tool from the registry. It also cleans up the toolset
// check if no other tools remain in the same toolset.
func (r *ToolRegistry) Deregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.tools[name]
	if !ok {
		return
	}
	delete(r.tools, name)

	// Remove toolset check if this was the last tool in that toolset.
	stillExists := false
	for _, e := range r.tools {
		if e.Toolset == entry.Toolset {
			stillExists = true
			break
		}
	}
	if !stillExists {
		delete(r.toolsetChecks, entry.Toolset)
	}
}

// ---------------------------------------------------------------------------
// Helpers for serialising tool responses. Every tool handler must return a
// JSON string. These helpers eliminate boilerplate.
//
// Usage:
//
//	registry.Register(...)
//	return toolError("something went wrong")
//	return toolError("not found", "code", 404)
//	return toolResult(success, "data", payload)
//	return toolResultData(payload)   // pass a dict directly

// toolError returns a JSON error string for tool handlers.
func toolError(message string, extra ...any) string {
	result := map[string]any{"error": message}
	for i := 0; i < len(extra)-1; i += 2 {
		result[fmt.Sprintf("%v", extra[i])] = extra[i+1]
	}
	buf, _ := json.Marshal(result)
	return string(buf)
}

// toolResult returns a JSON result string for tool handlers.
// When called with a single map argument, that map is serialised directly.
func toolResult(data ...any) string {
	var result map[string]any
	if len(data) == 1 {
		if m, ok := data[0].(map[string]any); ok {
			result = m
		}
	} else if len(data) >= 2 {
		result = make(map[string]any)
		for i := 0; i < len(data)-1; i += 2 {
			result[fmt.Sprintf("%v", data[i])] = data[i+1]
		}
	}
	buf, _ := json.Marshal(result)
	return string(buf)
}

// toolResultData serialises a single value as a JSON result with the key "data".
func toolResultData(v any) string {
	return toolResult("success", true, "data", v)
}

// ---------------------------------------------------------------------------
// Package-level singleton registry.
//
// Agent code should use this exported instance rather than creating its own.
var Registry = NewRegistry()

// Register is a convenience wrapper around Registry.Register that copies the
// arguments so that the caller does not need to import Registry explicitly.
func Register(
	name, toolset string,
	schema map[string]any,
	handler func(args map[string]any) string,
	checkFn func() bool,
	requiresEnv []string,
	isAsync bool,
	description, emoji string,
) {
	Registry.Register(name, toolset, schema, handler, checkFn, requiresEnv, isAsync, description, emoji)
}

// Call is a convenience wrapper around Registry.Call.
func Call(name string, args map[string]any) ToolResult {
	return Registry.Call(name, args)
}

// List is a convenience wrapper around Registry.List.
func List() []string {
	return Registry.List()
}

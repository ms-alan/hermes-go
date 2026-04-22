package tools

import (
	"testing"
)

func TestToolResultData(t *testing.T) {
	result := toolResultData(map[string]any{"foo": "bar"})
	if result == "" {
		t.Fatal("toolResultData returned empty string")
	}
	if !contains(result, "foo") || !contains(result, "bar") {
		t.Errorf("toolResultData = %q, want JSON with foo and bar", result)
	}
}

func TestToolError(t *testing.T) {
	result := toolError("not found")
	if !contains(result, "not found") {
		t.Errorf("toolError = %q, want JSON containing 'not found'", result)
	}
}

func TestToolResult(t *testing.T) {
	// Single map argument
	result := toolResult(map[string]any{"key": "value"})
	if !contains(result, "key") || !contains(result, "value") {
		t.Errorf("toolResult = %q, want JSON with key/value", result)
	}

	// Key-value pairs
	result = toolResult("code", 404, "msg", "not found")
	if !contains(result, "code") || !contains(result, "404") {
		t.Errorf("toolResult = %q, want JSON with code=404", result)
	}

	// Empty
	result = toolResult()
	if result == "" {
		t.Error("toolResult with no args returned empty, want '{}'")
	}
}

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}
	if len(r.List()) != 0 {
		t.Errorf("fresh registry has %d tools, want 0", len(r.List()))
	}
}

func TestRegistryRegisterAndList(t *testing.T) {
	r := NewRegistry()
	r.Register("test_tool", "builtin",
		map[string]any{"name": "test_tool", "description": "a test"},
		func(args map[string]any) string { return "{}" },
		nil, nil, false, "test", "🧪")

	names := r.List()
	if len(names) != 1 || names[0] != "test_tool" {
		t.Errorf("List = %v, want [test_tool]", names)
	}
}

func TestRegistryGetEntry(t *testing.T) {
	r := NewRegistry()
	r.Register("foo", "builtin",
		map[string]any{"name": "foo", "description": "desc"},
		func(args map[string]any) string { return "{}" },
		nil, nil, false, "desc", "📦")

	entry := r.GetEntry("foo")
	if entry == nil {
		t.Fatal("GetEntry(foo) returned nil")
	}
	if entry.Name != "foo" {
		t.Errorf("entry.Name = %q, want foo", entry.Name)
	}
	if entry.Description != "desc" {
		t.Errorf("entry.Description = %q, want desc", entry.Description)
	}

	if r.GetEntry("nonexistent") != nil {
		t.Error("GetEntry(nonexistent) should return nil")
	}
}

func TestRegistryCall(t *testing.T) {
	r := NewRegistry()
	r.Register("echo", "builtin",
		map[string]any{"name": "echo"},
		func(args map[string]any) string { return `{"echo": "ok"}` },
		nil, nil, false, "", "")

	result := r.Call("echo", map[string]any{})
	if !result.Success {
		t.Errorf("Call(echo) success=false, want true; error=%q", result.Error)
	}
	if !contains(result.Output, "echo") {
		t.Errorf("Call(echo) output = %q, want JSON with echo", result.Output)
	}

	// Unknown tool
	result = r.Call("unknown", nil)
	if result.Success {
		t.Error("Call(unknown) should report failure")
	}
	if !contains(result.Error, "unknown tool") {
		t.Errorf("Call(unknown) error = %q, want 'unknown tool'", result.Error)
	}
}

func TestRegistryCallCheckFn(t *testing.T) {
	r := NewRegistry()
	r.Register("unavail", "builtin",
		map[string]any{"name": "unavail"},
		func(args map[string]any) string { return "{}" },
		func() bool { return false }, // always unavailable
		nil, false, "", "")

	result := r.Call("unavail", nil)
	if result.Success {
		t.Error("Call(unavail) should fail when CheckFn returns false")
	}
}

func TestRegistryDeregister(t *testing.T) {
	r := NewRegistry()
	r.Register("delme", "builtin",
		map[string]any{"name": "delme"},
		func(args map[string]any) string { return "{}" },
		nil, nil, false, "", "")

	r.Deregister("delme")
	if len(r.List()) != 0 {
		t.Errorf("after Deregister(delme): List = %v, want []", r.List())
	}
}

func TestRegistryGetDefinitions(t *testing.T) {
	r := NewRegistry()
	r.Register("def_test", "builtin",
		map[string]any{
			"name":        "def_test",
			"description": "test desc",
			"parameters":  map[string]any{"type": "object"},
		},
		func(args map[string]any) string { return "{}" },
		nil, nil, false, "", "")

	defs := r.GetDefinitions([]string{"def_test"})
	if len(defs) != 1 {
		t.Fatalf("GetDefinitions([def_test]) returned %d items, want 1", len(defs))
	}
	if fn, ok := defs[0]["function"].(map[string]any); ok {
		if fn["name"] != "def_test" {
			t.Errorf("function.name = %v, want def_test", fn["name"])
		}
	} else {
		t.Error("def[function] is not a map")
	}
}

func TestRegistryGetToolsetForTool(t *testing.T) {
	r := NewRegistry()
	r.Register("ts_tool", "mcp-custom",
		map[string]any{"name": "ts_tool"},
		func(args map[string]any) string { return "{}" },
		nil, nil, false, "", "")

	if ts := r.GetToolsetForTool("ts_tool"); ts != "mcp-custom" {
		t.Errorf("GetToolsetForTool(ts_tool) = %q, want mcp-custom", ts)
	}
}

func TestRegistryGetToolNamesForToolset(t *testing.T) {
	r := NewRegistry()
	r.Register("tool_a", "ts1", map[string]any{"name": "tool_a"}, func(args map[string]any) string { return "{}" }, nil, nil, false, "", "")
	r.Register("tool_b", "ts1", map[string]any{"name": "tool_b"}, func(args map[string]any) string { return "{}" }, nil, nil, false, "", "")
	r.Register("tool_c", "ts2", map[string]any{"name": "tool_c"}, func(args map[string]any) string { return "{}" }, nil, nil, false, "", "")

	names := r.GetToolNamesForToolset("ts1")
	if len(names) != 2 {
		t.Errorf("GetToolNamesForToolset(ts1) = %v, want 2 tools", names)
	}
}

func TestIsMCP(t *testing.T) {
	for _, tc := range []struct {
		toolset string
		want    bool
	}{
		{"mcp-server", true},
		{"mcp-custom", true},
		{"builtin", false},
		{"openai", false},
	} {
		if got := isMCP(tc.toolset); got != tc.want {
			t.Errorf("isMCP(%q) = %v, want %v", tc.toolset, got, tc.want)
		}
	}
}

func TestSchemaToToolSchema(t *testing.T) {
	schema := schemaToToolSchema(map[string]any{
		"name":        "my_tool",
		"description": "does things",
		"parameters":  map[string]any{"type": "object"},
	}, "my_tool")
	if schema.Name != "my_tool" {
		t.Errorf("schema.Name = %q, want my_tool", schema.Name)
	}
	if schema.Description != "does things" {
		t.Errorf("schema.Description = %q, want does things", schema.Description)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

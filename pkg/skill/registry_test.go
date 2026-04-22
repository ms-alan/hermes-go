package skill

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// mockAgent implements AgentInterface for testing.
type mockAgent struct{}

func (m *mockAgent) Chat(ctx context.Context, msg string) (string, error) {
	return "mock response: " + msg, nil
}
func (m *mockAgent) SystemPrompt() string { return "mock system prompt" }

func loggerForTest() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestRegister(t *testing.T) {
	r := &Registry{skills: make(map[string]*Skill), logger: loggerForTest()}
	err := r.register(&Skill{Name: "test_skill", Description: "a test skill"})
	if err != nil {
		t.Fatal(err)
	}
	if r.skills["test_skill"] == nil {
		t.Error("skill not registered")
	}
}

func TestRegisterDuplicate(t *testing.T) {
	r := &Registry{skills: make(map[string]*Skill), logger: loggerForTest()}
	r.register(&Skill{Name: "dup", Description: "first"})
	err := r.register(&Skill{Name: "dup", Description: "second"})
	if err == nil {
		t.Error("expected error on duplicate registration, got nil")
	}
}

func TestRegistryGet(t *testing.T) {
	r := &Registry{skills: map[string]*Skill{
		"get_test": {Name: "get_test", Description: "desc"},
	}, logger: loggerForTest()}
	s := r.Get("get_test")
	if s == nil || s.Name != "get_test" {
		t.Errorf("Get(get_test) = %v, want skill with Name=get_test", s)
	}
	if r.Get("nonexistent") != nil {
		t.Error("Get(nonexistent) should return nil")
	}
}

func TestRegistryGetByCommand(t *testing.T) {
	r := &Registry{skills: map[string]*Skill{
		"cmd_skill": {Name: "cmd_skill", Commands: []string{"news", "ainews"}},
	}, logger: loggerForTest()}
	s := r.GetByCommand("news")
	if s == nil || s.Name != "cmd_skill" {
		t.Errorf("GetByCommand(news) = %v, want cmd_skill", s)
	}
	if r.GetByCommand("unknown") != nil {
		t.Error("GetByCommand(unknown) should return nil")
	}
}

func TestRegistryList(t *testing.T) {
	r := &Registry{skills: map[string]*Skill{
		"skill1": {Name: "skill1"},
		"skill2": {Name: "skill2"},
	}, logger: loggerForTest()}
	list := r.List()
	if len(list) != 2 {
		t.Errorf("List() returned %d skills, want 2", len(list))
	}
}

func TestParseSKILLMD(t *testing.T) {
	content := `
name: my-skill
description: A test skill for unit testing
commands: [test, unit]
runtime: shell
entry: run.sh
`
	name, desc, commands, runtime, entry := parseSKILLMD(content)
	if name != "my-skill" {
		t.Errorf("name = %q, want my-skill", name)
	}
	if desc != "A test skill for unit testing" {
		t.Errorf("description = %q, want 'A test skill for unit testing'", desc)
	}
	if len(commands) != 2 || commands[0] != "test" || commands[1] != "unit" {
		t.Errorf("commands = %v, want [test unit]", commands)
	}
	if runtime != "shell" {
		t.Errorf("runtime = %q, want shell", runtime)
	}
	if entry != "run.sh" {
		t.Errorf("entry = %q, want run.sh", entry)
	}
}

func TestParseSKILLMDMinimal(t *testing.T) {
	content := `
name: minimal
`
	name, _, _, runtime, entry := parseSKILLMD(content)
	if name != "minimal" {
		t.Errorf("name = %q, want minimal", name)
	}
	if runtime != "" {
		t.Errorf("runtime = %q, want empty", runtime)
	}
	if entry != "" {
		t.Errorf("entry = %q, want empty", entry)
	}
}

func TestParseBracketList(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{`[a, b]`, []string{"a", "b"}},
		{`[single]`, []string{"single"}},
		{`[]`, nil},
		{`no brackets`, nil},
		{`  [  spaced  , b]`, []string{"spaced", "b"}},
	}
	for _, tc := range tests {
		got := parseBracketList(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("parseBracketList(%q) = %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseBracketList(%q) = %v, want %v", tc.input, got, tc.want)
			}
		}
	}
}

func TestLoaderLoadAllMissingDir(t *testing.T) {
	l := NewLoader("/nonexistent/path/12345", nil)
	err := l.LoadAll()
	if err != nil {
		t.Errorf("LoadAll() on nonexistent dir returned error: %v (should be nil)", err)
	}
}

func TestLoaderLoadAllWithTempDir(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "skills")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `
name: unit-test-skill
description: A skill for testing
commands: ["ut"]
runtime: shell
entry: run.sh
`
	skillPath := filepath.Join(skillDir, "unit-test-skill")
	if err := os.Mkdir(skillPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillPath, "SKILL.md"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillPath, "run.sh"), []byte("#!/bin/sh\necho ok"), 0755); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(skillDir, loggerForTest())
	_ = l.LoadAll() // just verify no panic
}

func TestLoaderLoadFromManifestMissingName(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "bad")
	os.Mkdir(dir, 0755)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("description: no name here\n"), 0644)
	l := NewLoader(tmp, loggerForTest())
	err := l.loadDir(dir)
	if err == nil {
		t.Error("loadDir with missing name should return error")
	}
}

func TestLoaderLoadFromJSONManifest(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "json-skill")
	os.Mkdir(dir, 0755)
	manifest := `{
  "name": "json-skill",
  "description": "loaded from JSON",
  "commands": ["js"],
  "runtime": "shell",
  "entry": "run.sh"
}`
	if err := os.WriteFile(filepath.Join(dir, "skill.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\necho json-skill-ok"), 0755); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(tmp, loggerForTest())
	_ = l.LoadAll() // just verify no panic
}

func TestLoaderLoadFromJSONManifestMissingFields(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "bad-json")
	os.Mkdir(dir, 0755)
	os.WriteFile(filepath.Join(dir, "skill.json"), []byte(`{"name": ""}`), 0644)
	l := NewLoader(tmp, loggerForTest())
	err := l.loadDir(dir)
	if err == nil {
		t.Error("loadDir with empty name should return error")
	}
}

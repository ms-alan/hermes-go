package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Loader handles loading skills from disk.
type Loader struct {
	SkillsDir string
	Logger    *slog.Logger
}

// NewLoader creates a skill loader for the given skills directory.
func NewLoader(skillsDir string, logger *slog.Logger) *Loader {
	if logger == nil {
		logger = slog.Default()
	}
	return &Loader{SkillsDir: skillsDir, Logger: logger}
}

// LoadAll loads all skills from the skills directory.
func (l *Loader) LoadAll() error {
	entries, err := os.ReadDir(l.SkillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read skills dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err := l.loadDir(filepath.Join(l.SkillsDir, entry.Name())); err != nil {
				l.Logger.Warn("failed to load skill dir", "dir", entry.Name(), "error", err)
			}
		}
	}
	return nil
}

func (l *Loader) loadDir(dir string) error {
	manifestPath := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(manifestPath); err == nil {
		return l.loadFromManifest(dir, manifestPath)
	}
	manifestPath = filepath.Join(dir, "skill.json")
	if _, err := os.Stat(manifestPath); err == nil {
		return l.loadFromJSONManifest(dir, manifestPath)
	}
	return nil
}

func (l *Loader) loadFromManifest(dir, manifestPath string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	name, desc, commands, rt, entry := parseSKILLMD(string(data))
	if name == "" {
		return fmt.Errorf("manifest missing name field")
	}
	if entry == "" {
		entry = "run.sh"
	}
	if rt == "" {
		rt = "shell"
	}

	var handler SkillHandler
	switch rt {
	case "shell":
		scriptPath := filepath.Join(dir, entry)
		handler = makeShellHandler(scriptPath)
	case "python":
		scriptPath := filepath.Join(dir, entry)
		handler = makePythonHandler(scriptPath)
	default:
		return fmt.Errorf("unsupported runtime: %s", rt)
	}

	Register(name, desc, commands, handler)
	l.Logger.Info("loaded skill", "name", name)
	return nil
}

func (l *Loader) loadFromJSONManifest(dir, manifestPath string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read JSON manifest: %w", err)
	}
	var manifest struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Commands    []string `json:"commands"`
		Runtime     string   `json:"runtime"`
		Entry       string   `json:"entry"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse JSON manifest: %w", err)
	}
	if manifest.Name == "" {
		return fmt.Errorf("manifest missing name field")
	}
	if manifest.Runtime == "" {
		manifest.Runtime = "shell"
	}
	if manifest.Entry == "" {
		return fmt.Errorf("manifest missing entry field")
	}

	var handler SkillHandler
	switch manifest.Runtime {
	case "shell":
		scriptPath := filepath.Join(dir, manifest.Entry)
		handler = makeShellHandler(scriptPath)
	case "python":
		scriptPath := filepath.Join(dir, manifest.Entry)
		handler = makePythonHandler(scriptPath)
	default:
		return fmt.Errorf("unsupported runtime: %s", manifest.Runtime)
	}

	Register(manifest.Name, manifest.Description, manifest.Commands, handler)
	l.Logger.Info("loaded skill", "name", manifest.Name)
	return nil
}

func parseSKILLMD(content string) (name, description string, commands []string, runtime, entry string) {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "name:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		} else if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		} else if strings.HasPrefix(line, "commands:") {
			commands = parseBracketList(strings.TrimSpace(strings.TrimPrefix(line, "commands:")))
		} else if strings.HasPrefix(line, "runtime:") {
			runtime = strings.TrimSpace(strings.TrimPrefix(line, "runtime:"))
		} else if strings.HasPrefix(line, "entry:") {
			entry = strings.TrimSpace(strings.TrimPrefix(line, "entry:"))
		}
	}
	return
}

func parseBracketList(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil
	}
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	var result []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(strings.Trim(item, "\""))
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

// LoadGoPlugin loads a Go plugin (.so file, Linux only).
// STUB: this function is not yet implemented and always returns an error.
// Real implementation requires runtime/plugin and a compiled .so file.
// Only available on Linux; returns an error on all other platforms.
func (l *Loader) LoadGoPlugin(path string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("Go plugins are only supported on Linux (current: %s)", runtime.GOOS)
	}
	// Uses plugin.Open — requires runtime/plugin import at call site.
	// This is a stub; real implementation needs to compile the skill as a
	// Go plugin (.so) and use plugin.Open to load the .so file.
	_ = path
	return fmt.Errorf("LoadGoPlugin: not yet implemented (Go plugin loading requires pre-compiled .so)")
}

// makeShellHandler creates a skill handler that runs a shell script.
func makeShellHandler(scriptPath string) SkillHandler {
	return func(ctx context.Context, args string, agent AgentInterface) (string, error) {
		cmd := exec.CommandContext(ctx, "bash", "-c", scriptPath+" "+args)
		cmd.Env = append(os.Environ(),
			"HERMES_SESSION_ID="+sessionIDFromAgent(agent),
			"HERMES_SYSTEM_PROMPT="+agent.SystemPrompt(),
		)
		output, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return "", fmt.Errorf("skill script exited: %s", string(ee.Stderr))
			}
			return "", fmt.Errorf("run shell skill: %w", err)
		}
		return string(output), nil
	}
}

// makePythonHandler creates a skill handler that runs a Python script.
func makePythonHandler(scriptPath string) SkillHandler {
	return func(ctx context.Context, args string, agent AgentInterface) (string, error) {
		cmd := exec.CommandContext(ctx, "python3", scriptPath, args)
		cmd.Env = append(os.Environ(),
			"HERMES_SESSION_ID="+sessionIDFromAgent(agent),
			"HERMES_SYSTEM_PROMPT="+agent.SystemPrompt(),
		)
		output, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return "", fmt.Errorf("skill script exited: %s", string(ee.Stderr))
			}
			return "", fmt.Errorf("run python skill: %w", err)
		}
		return string(output), nil
	}
}

func sessionIDFromAgent(agent AgentInterface) string {
	if agent == nil {
		return ""
	}
	// AgentInterface doesn't expose SessionID directly
	// Skills that need it can read HERMES_SESSION_ID env var
	return ""
}

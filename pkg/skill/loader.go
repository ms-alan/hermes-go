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
	// skillDirs maps skill name → its directory path (populated during LoadAll).
	skillDirs map[string]string
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

	// Detect YAML frontmatter (--- ... ---) or simple key-value format.
	var skill *Skill
	if strings.Contains(string(data), "---") {
		skill, err = l.parseYAMLFrontmatter(string(data), dir)
		if err != nil {
			return fmt.Errorf("parse YAML frontmatter: %w", err)
		}
	} else {
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
		skill = l.buildSkill(name, desc, commands, rt, entry, dir)
	}

	RegisterSkill(skill)
	l.Logger.Info("loaded skill", "name", skill.Name)
	return nil
}

// parseYAMLFrontmatter parses a SKILL.md with YAML frontmatter (--- ... ---).
// The frontmatter fields are: name, brief_description, description, commands,
// runtime, entry, version, author, license, category, platforms, prerequisites,
// tags, config, tier.
func (l *Loader) parseYAMLFrontmatter(content, dir string) (*Skill, error) {
	// Extract frontmatter block between --- markers.
	lines := strings.Split(content, "\n")
	var frontmatterLines []string
	inFrontmatter := false
	bodyLines := []string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "---") {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			// End of frontmatter.
			break
		}
		if inFrontmatter {
			frontmatterLines = append(frontmatterLines, line)
		} else {
			bodyLines = append(bodyLines, line)
		}
	}

	// Parse frontmatter as simple key: value pairs.
	fm := make(map[string]string)
	for _, fl := range frontmatterLines {
		fl = strings.TrimSpace(fl)
		if fl == "" || strings.HasPrefix(fl, "#") {
			continue
		}
		idx := strings.Index(fl, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(fl[:idx])
		val := strings.TrimSpace(fl[idx+1:])
		fm[key] = val
	}

	name := fm["name"]
	if name == "" {
		return nil, fmt.Errorf("frontmatter missing name")
	}

	briefDesc := fm["brief_description"]
	desc := fm["description"]
	commands := parseBracketList(fm["commands"])
	runtime := fm["runtime"]
	entry := fm["entry"]
	version := fm["version"]
	author := fm["author"]
	license := fm["license"]
	category := fm["category"]
	platforms := parseBracketList(fm["platforms"])
	prerequisites := parseBracketList(fm["prerequisites"])
	tags := parseBracketList(fm["tags"])
	config := fm["config"]

	if runtime == "" {
		runtime = "shell"
	}
	if entry == "" {
		entry = "run.sh"
	}

	skill := l.buildSkill(name, desc, commands, runtime, entry, dir)
	skill.BriefDescription = briefDesc
	if skill.BriefDescription == "" {
		skill.BriefDescription = desc
	}
	skill.Version = version
	skill.Author = author
	skill.License = license
	skill.Category = category
	skill.Platforms = platforms
	skill.Prerequisites = prerequisites
	skill.Tags = tags
	skill.Config = config

	// Parse tier field.
	switch fm["tier"] {
	case "1":
		skill.Tier = Tier1Brief
	case "3":
		skill.Tier = Tier3Full
	default:
		skill.Tier = Tier2Metadata
	}

	// Store dir for linked file loading.
	if l.skillDirs == nil {
		l.skillDirs = make(map[string]string)
	}
	l.skillDirs[name] = dir

	return skill, nil
}

// buildSkill creates a Skill with the given metadata and a handler for the runtime.
func (l *Loader) buildSkill(name, desc string, commands []string, runtime, entry, dir string) *Skill {
	var handler SkillHandler
	switch runtime {
	case "shell":
		scriptPath := filepath.Join(dir, entry)
		handler = makeShellHandler(scriptPath)
	case "python":
		scriptPath := filepath.Join(dir, entry)
		handler = makePythonHandler(scriptPath)
	default:
		handler = func(ctx context.Context, args string, agent AgentInterface) (string, error) {
			return "", fmt.Errorf("unsupported runtime: %s", runtime)
		}
	}

	return &Skill{
		Name:             name,
		BriefDescription: desc,
		Description:      desc,
		Commands:         commands,
		Handler:          handler,
		Tier:             Tier2Metadata,
		LinkedFiles:      make(map[string]string),
	}
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

// LoadLinkedFiles loads linked files (references/, templates/, scripts/, assets/)
// for a named skill. Returns the populated LinkedFiles map.
func (l *Loader) LoadLinkedFiles(name string) (map[string]string, error) {
	dir, ok := l.skillDirs[name]
	if !ok {
		return nil, fmt.Errorf("skill %q: directory not known (load it first)", name)
	}

	linkedDirs := []string{"references", "templates", "scripts", "assets"}
	result := make(map[string]string)

	for _, ld := range linkedDirs {
		ldPath := filepath.Join(dir, ld)
		entries, err := os.ReadDir(ldPath)
		if err != nil {
			continue // dir doesn't exist
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			filePath := filepath.Join(ldPath, entry.Name())
			content, err := os.ReadFile(filePath)
			if err != nil {
				continue
			}
			relPath := filepath.Join(ld, entry.Name())
			result[relPath] = string(content)
		}
	}
	return result, nil
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

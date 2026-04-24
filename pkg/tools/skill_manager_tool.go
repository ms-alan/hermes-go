package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nousresearch/hermes-go/pkg/skill"
)

// skillManagerSchema defines the skill management tool.
var skillManageSchema = map[string]any{
	"name":        "skill_manage",
	"description": "Create, edit, patch, and delete skills — turning successful approaches into reusable procedural knowledge. Skills capture how to do a specific type of task. User skills live in ~/.hermes/skills/. Actions: create (new skill with SKILL.md), edit (replace SKILL.md), patch (find-replace), delete (remove skill), write_file (add supporting file), remove_file (delete supporting file).",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action: create, edit, patch, delete, write_file, or remove_file",
				"enum":        []any{"create", "edit", "patch", "delete", "write_file", "remove_file"},
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Skill name (e.g. 'my-skill'). Required for create/delete/write_file/remove_file.",
			},
			"category": map[string]any{
				"type":        "string",
				"description": "Category directory for the skill (e.g. 'research', 'devops'). Optional for create.",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "SKILL.md content for create/edit actions. Must start with YAML frontmatter (---).",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "File path for write_file/remove_file (e.g. 'references/api.md').",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "Text to find for patch action.",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "Replacement text for patch action.",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "Replace all occurrences for patch (default: false).",
			},
		},
		"required": []any{"action"},
	},
}

var validNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func skillManageHandler(args map[string]any) string {
	action, _ := args["action"].(string)

	switch action {
	case "create":
		return skillCreate(args)
	case "edit":
		return skillEdit(args)
	case "patch":
		return skillPatch(args)
	case "delete":
		return skillDelete(args)
	case "write_file":
		return skillWriteFile(args)
	case "remove_file":
		return skillRemoveFile(args)
	default:
		return toolError(fmt.Sprintf("unknown action %q — use: create, edit, patch, delete, write_file, remove_file", action))
	}
}

func skillDir(name, category string) (string, error) {
	base := os.Getenv("HERMES_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot find home dir: %w", err)
		}
		base = filepath.Join(home, ".hermes")
	}
	skillsDir := filepath.Join(base, "skills")
	if category != "" {
		skillsDir = filepath.Join(skillsDir, category)
	}
	if name != "" {
		skillsDir = filepath.Join(skillsDir, name)
	}
	return skillsDir, nil
}

func validateSkillName(name string) string {
	if name == "" {
		return "skill name is required"
	}
	if len(name) > 64 {
		return "skill name exceeds 64 characters"
	}
	if !validNameRe.MatchString(name) {
		return "invalid skill name — use lowercase letters, numbers, hyphens, dots, underscores; must start with letter or digit"
	}
	return ""
}

func validateCategory(cat string) string {
	if cat == "" {
		return ""
	}
	if strings.Contains(cat, "/") || strings.Contains(cat, "\\") {
		return "category must be a single directory name (no slashes)"
	}
	if len(cat) > 64 {
		return "category exceeds 64 characters"
	}
	if !validNameRe.MatchString(cat) {
		return "invalid category name"
	}
	return ""
}

func skillCreate(args map[string]any) string {
	name, _ := args["name"].(string)
	category, _ := args["category"].(string)
	content, _ := args["content"].(string)

	if err := validateSkillName(name); err != "" {
		return toolError(err)
	}
	if err := validateCategory(category); err != "" {
		return toolError(err)
	}
	if content == "" {
		return toolError("SKILL.md content is required for create")
	}
	if !strings.HasPrefix(content, "---") {
		return toolError("SKILL.md must start with YAML frontmatter (---)")
	}

	dir, err := skillDir(name, category)
	if err != nil {
		return toolError(err.Error())
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return toolError(fmt.Sprintf("create skill dir: %v", err))
	}

	skillPath := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
		return toolError(fmt.Sprintf("write SKILL.md: %v", err))
	}

	// Create allowed subdirectories
	for _, subdir := range []string{"references", "templates", "scripts", "assets"} {
		os.MkdirAll(filepath.Join(dir, subdir), 0755)
	}

	// Reload skill registry
	ldr := skill.GetLoader()
	if ldr != nil {
		ldr.LoadAll()
	}

	return toolResult("created", map[string]any{
		"skill":  name,
		"dir":    dir,
		"action": "create",
	})
}

func skillEdit(args map[string]any) string {
	name, _ := args["name"].(string)
	content, _ := args["content"].(string)

	if err := validateSkillName(name); err != "" {
		return toolError(err)
	}
	if content == "" {
		return toolError("SKILL.md content is required for edit")
	}
	if !strings.HasPrefix(content, "---") {
		return toolError("SKILL.md must start with YAML frontmatter (---)")
	}

	// Find the skill — look in ~/.hermes/skills/
	base := os.Getenv("HERMES_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".hermes")
	}
	skillsDir := filepath.Join(base, "skills")

	// Find skill dir by walking skills dir
	var skillPath string
	filepath.Walk(skillsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() == "SKILL.md" {
			rel, _ := filepath.Rel(skillsDir, filepath.Dir(path))
			parts := strings.Split(rel, string(filepath.Separator))
			if len(parts) >= 1 && parts[0] == name {
				skillPath = path
				return filepath.SkipAll
			}
		}
		return nil
	})

	if skillPath == "" {
		return toolError(fmt.Sprintf("skill %q not found in ~/.hermes/skills/", name))
	}

	if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
		return toolError(fmt.Sprintf("write SKILL.md: %v", err))
	}

	// Reload skill registry
	ldr := skill.GetLoader()
	if ldr != nil {
		ldr.LoadAll()
	}

	return toolResult("edited", map[string]any{
		"skill":  name,
		"action": "edit",
	})
}

func skillPatch(args map[string]any) string {
	name, _ := args["name"].(string)
	path, _ := args["path"].(string) // relative to skill dir, e.g. "references/api.md"
	oldStr, _ := args["old_string"].(string)
	newStr, _ := args["new_string"].(string)
	replaceAll, _ := args["replace_all"].(bool)

	if err := validateSkillName(name); err != "" {
		return toolError(err)
	}
	if oldStr == "" {
		return toolError("old_string is required for patch")
	}

	// Find the skill dir
	base := os.Getenv("HERMES_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".hermes")
	}
	skillsDir := filepath.Join(base, "skills")

	var skillDirPath string
	filepath.Walk(skillsDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() == "SKILL.md" {
			rel, _ := filepath.Rel(skillsDir, filepath.Dir(p))
			parts := strings.Split(rel, string(filepath.Separator))
			if len(parts) >= 1 && parts[0] == name {
				skillDirPath = filepath.Dir(p)
				return filepath.SkipAll
			}
		}
		return nil
	})

	if skillDirPath == "" {
		return toolError(fmt.Sprintf("skill %q not found", name))
	}

	// Default to SKILL.md
	filePath := filepath.Join(skillDirPath, "SKILL.md")
	if path != "" {
		filePath = filepath.Join(skillDirPath, path)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return toolError(fmt.Sprintf("read file: %v", err))
	}
	content := string(data)

	count := 1
	if replaceAll {
		count = strings.Count(content, oldStr)
		if count == 0 {
			return toolError("old_string not found")
		}
		content = strings.Replace(content, oldStr, newStr, -1)
	} else {
		if !strings.Contains(content, oldStr) {
			return toolError("old_string not found")
		}
		idx := strings.Index(content, oldStr)
		content = content[:idx] + newStr + content[idx+len(oldStr):]
	}

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return toolError(fmt.Sprintf("write file: %v", err))
	}

	// Reload skill registry
	ldr := skill.GetLoader()
	if ldr != nil {
		ldr.LoadAll()
	}

	return toolResult("patched", map[string]any{
		"skill":    name,
		"file":     path,
		"replaced": count,
		"action":   "patch",
	})
}

func skillDelete(args map[string]any) string {
	name, _ := args["name"].(string)

	if err := validateSkillName(name); err != "" {
		return toolError(err)
	}

	base := os.Getenv("HERMES_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".hermes")
	}
	skillsDir := filepath.Join(base, "skills")

	// Find skill dir
	var skillDirPath string
	filepath.Walk(skillsDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(skillsDir, p)
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) >= 1 && parts[0] == name && p != skillsDir {
			skillDirPath = p
			return filepath.SkipAll
		}
		return nil
	})

	if skillDirPath == "" {
		return toolError(fmt.Sprintf("skill %q not found", name))
	}

	// Only allow deleting skills within SKILLS_DIR (not external dirs)
	rel, _ := filepath.Rel(skillsDir, skillDirPath)
	if strings.HasPrefix(rel, "..") {
		return toolError("can only delete skills within ~/.hermes/skills/")
	}

	if err := os.RemoveAll(skillDirPath); err != nil {
		return toolError(fmt.Sprintf("remove skill dir: %v", err))
	}

	// Reload skill registry
	ldr := skill.GetLoader()
	if ldr != nil {
		ldr.LoadAll()
	}

	return toolResult("deleted", map[string]any{
		"skill":  name,
		"action": "delete",
	})
}

func skillWriteFile(args map[string]any) string {
	name, _ := args["name"].(string)
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)

	if err := validateSkillName(name); err != "" {
		return toolError(err)
	}
	if path == "" {
		return toolError("path is required for write_file")
	}
	if content == "" {
		return toolError("content is required for write_file")
	}

	// Allowed subdirs: references, templates, scripts, assets
	allowed := map[string]bool{"references": true, "templates": true, "scripts": true, "assets": true}
	parts := strings.SplitN(path, "/", 2)
	if !allowed[parts[0]] && len(parts) > 1 {
		return toolError("write_file can only write to: references/, templates/, scripts/, assets/")
	}

	base := os.Getenv("HERMES_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".hermes")
	}
	skillsDir := filepath.Join(base, "skills")

	var skillDirPath string
	filepath.Walk(skillsDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() == "SKILL.md" {
			rel, _ := filepath.Rel(skillsDir, filepath.Dir(p))
			parts := strings.Split(rel, string(filepath.Separator))
			if len(parts) >= 1 && parts[0] == name {
				skillDirPath = filepath.Dir(p)
				return filepath.SkipAll
			}
		}
		return nil
	})

	if skillDirPath == "" {
		return toolError(fmt.Sprintf("skill %q not found", name))
	}

	fullPath := filepath.Join(skillDirPath, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return toolError(fmt.Sprintf("create dir: %v", err))
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return toolError(fmt.Sprintf("write file: %v", err))
	}

	return toolResult("written", map[string]any{
		"skill": name,
		"path":  path,
		"file":  fullPath,
		"action": "write_file",
	})
}

func skillRemoveFile(args map[string]any) string {
	name, _ := args["name"].(string)
	path, _ := args["path"].(string)

	if err := validateSkillName(name); err != "" {
		return toolError(err)
	}
	if path == "" {
		return toolError("path is required for remove_file")
	}

	base := os.Getenv("HERMES_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".hermes")
	}
	skillsDir := filepath.Join(base, "skills")

	var skillDirPath string
	filepath.Walk(skillsDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() == "SKILL.md" {
			rel, _ := filepath.Rel(skillsDir, filepath.Dir(p))
			parts := strings.Split(rel, string(filepath.Separator))
			if len(parts) >= 1 && parts[0] == name {
				skillDirPath = filepath.Dir(p)
				return filepath.SkipAll
			}
		}
		return nil
	})

	if skillDirPath == "" {
		return toolError(fmt.Sprintf("skill %q not found", name))
	}

	fullPath := filepath.Join(skillDirPath, path)
	if err := os.Remove(fullPath); err != nil {
		return toolError(fmt.Sprintf("remove file: %v", err))
	}

	return toolResult("removed", map[string]any{
		"skill":  name,
		"path":   path,
		"action": "remove_file",
	})
}

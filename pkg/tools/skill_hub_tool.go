package tools

import (
	"fmt"
	"strings"

	"github.com/nousresearch/hermes-go/pkg/skill"
)

// ============================================================================
// Skill Hub Tool — search and install remote skills from GitHub
// ============================================================================

var skillHubSchema = map[string]any{
	"name":        "skill_hub",
	"description": "Search the remote skill hub (GitHub taps) for installable skills and install them locally. Searches openai/skills, anthropics/skills, VoltAgent/awesome-agent-skills, and more. After installation the skill is immediately available via skill_list. Use 'search' to find skills, 'install' to install them, 'inspect' to preview a skill's metadata without downloading.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action: search, install, inspect, or list_taps",
				"enum":        []any{"search", "install", "inspect", "list_taps"},
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Search query (for action=search). Matches skill name, description, and tags.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Max results to return (default: 10, max: 30).",
				"default":     10,
			},
			"identifier": map[string]any{
				"type":        "string",
				"description": "Skill identifier like 'owner/repo/path/to/skill-dir' (for action=install or inspect).",
			},
			"skills_dir": map[string]any{
				"type":        "string",
				"description": "Local skills directory (default: ~/.hermes/skills).",
			},
		},
		"required": []any{"action"},
	},
}

// hub is the global Hub instance.
var hub *skill.Hub

func getHub() *skill.Hub {
	if hub == nil {
		hub = skill.NewHub(nil, nil, nil)
	}
	return hub
}

func skillHubHandler(args map[string]any) string {
	action, _ := args["action"].(string)
	action = strings.TrimSpace(action)

	switch action {
	case "search":
		return hubSearch(args)
	case "install":
		return hubInstall(args)
	case "inspect":
		return hubInspect(args)
	case "list_taps":
		return hubListTaps(args)
	default:
		return toolError(fmt.Sprintf("unknown action %q — use: search, install, inspect, list_taps", action))
	}
}

func hubSearch(args map[string]any) string {
	query, _ := args["query"].(string)
	if query == "" {
		return toolError("query is required for search action")
	}

	limit := 10
	if v, ok := args["limit"].(float64); ok && int(v) > 0 {
		limit = int(v)
	}
	if limit > 30 {
		limit = 30
	}

	results, err := getHub().Search(query, limit)
	if err != nil {
		return toolError("hub search failed: " + err.Error())
	}

	if len(results) == 0 {
		return toolResultData(map[string]any{
			"success": true,
			"results": []any{},
			"message": "no skills found matching " + query,
		})
	}

	type ResultOut struct {
		Name         string   `json:"name"`
		Description  string   `json:"description"`
		Identifier   string   `json:"identifier"`
		TrustLevel   string   `json:"trust_level"`
		Tags         []string `json:"tags"`
		InstallHint  string   `json:"install_hint"`
	}
	out := make([]ResultOut, len(results))
	for i, r := range results {
		out[i] = ResultOut{
			Name:        r.Name,
			Description: r.Description,
			Identifier:  r.Identifier,
			TrustLevel:  r.TrustLevel,
			Tags:        r.Tags,
			InstallHint: "skill_hub(action=install, identifier=" + r.Identifier + ")",
		}
	}
	return toolResultData(map[string]any{
		"success": true,
		"results": out,
		"count":   len(out),
	})
}

func hubInstall(args map[string]any) string {
	identifier, _ := args["identifier"].(string)
	if identifier == "" {
		return toolError("identifier is required for install action")
	}

	skillsDir, _ := args["skills_dir"].(string)

	bundle, err := getHub().Fetch(identifier)
	if err != nil {
		return toolError("hub fetch failed: " + err.Error())
	}

	if err := getHub().Install(bundle, skillsDir); err != nil {
		return toolError("hub install failed: " + err.Error())
	}

	return toolResultData(map[string]any{
		"success":     true,
		"name":        bundle.Name,
		"trust_level": bundle.TrustLevel,
		"file_count":  len(bundle.Files),
	})
}

func hubInspect(args map[string]any) string {
	identifier, _ := args["identifier"].(string)
	if identifier == "" {
		return toolError("identifier is required for inspect action")
	}

	bundle, err := getHub().Fetch(identifier)
	if err != nil {
		return toolError("hub inspect failed: " + err.Error())
	}

	// Extract SKILL.md content
	var skillMD string
	if content, ok := bundle.Files["SKILL.md"]; ok {
		skillMD = string(content)
		// Truncate if too long
		if len(skillMD) > 3000 {
			skillMD = skillMD[:3000] + "\n\n...(truncated)"
		}
	}

	type FileInfo struct {
		Name         string `json:"name"`
		Size         int    `json:"size"`
		IsSKILLMD    bool   `json:"is_skill_md"`
	}
	var files []FileInfo
	for name, content := range bundle.Files {
		files = append(files, FileInfo{
			Name:      name,
			Size:      len(content),
			IsSKILLMD: name == "SKILL.md",
		})
	}

	return toolResultData(map[string]any{
		"success":     true,
		"name":        bundle.Name,
		"identifier":  bundle.Identifier,
		"trust_level": bundle.TrustLevel,
		"file_count":  len(bundle.Files),
		"skill_md":    skillMD,
		"files":       files,
	})
}

func hubListTaps(args map[string]any) string {
	taps := getHub().Taps
	type TapOut struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
	}
	out := make([]TapOut, len(taps))
	for i, t := range taps {
		out[i] = TapOut{Repo: t.Repo, Path: t.Path}
	}
	return toolResultData(map[string]any{
		"success": true,
		"taps":    out,
	})
}

func skillHubAvailable() bool {
	return true // Hub always available, auth is lazy
}

func init() {
	Register("skill_hub", "skill", skillHubSchema, skillHubHandler, skillHubAvailable,
		nil, false,
		"Search and install remote skills from GitHub taps (openai/skills, anthropics/skills, etc.)", "🔍")
}

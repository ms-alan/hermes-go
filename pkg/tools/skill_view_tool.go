package tools

import (
	"encoding/json"

	"github.com/nousresearch/hermes-go/pkg/skill"
)

var skillViewSchema = map[string]any{
	"name":        "skill_view",
	"description": "View detailed skill metadata (tier 2) or load linked file content (tier 3). Without file_path returns full metadata. With file_path returns the actual content of a linked file.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Skill name (e.g. 'ai-daily-news')",
			},
			"file_path": map[string]any{
				"type":        "string",
				"description": "Optional path to a linked file inside the skill (e.g. 'references/api.md'). If omitted, returns full tier-2 metadata.",
			},
		},
		"required": []any{"name"},
	},
}

func skillViewHandler(args map[string]any) string {
	name, ok := args["name"].(string)
	if !ok || name == "" {
		return toolError("skill name is required")
	}
	filePath, hasFilePath := args["file_path"].(string)

	s := skill.Get(name)
	if s == nil {
		return toolError("skill " + name + " not found")
	}

	if hasFilePath && filePath != "" {
		content, err := skill.GetSkillLinkedFile(name, filePath)
		if err != nil {
			return toolError(err.Error())
		}
		result := map[string]any{
			"success":   true,
			"name":      name,
			"file_path": filePath,
			"content":   content,
		}
		data, _ := json.Marshal(result)
		return string(data)
	}

	result := map[string]any{
		"success":           true,
		"name":              s.Name,
		"brief_description": s.BriefDescription,
		"description":       s.Description,
		"commands":          s.Commands,
		"version":           s.Version,
		"author":            s.Author,
		"license":           s.License,
		"category":          s.Category,
		"platforms":         s.Platforms,
		"prerequisites":     s.Prerequisites,
		"tags":              s.Tags,
		"config":            s.Config,
		"tier":              int(s.Tier),
		"hint":              "Use skill_view(name, file_path) to load linked files (references/, templates/, scripts/, assets/)",
	}
	data, _ := json.Marshal(result)
	return string(data)
}

package tools

import (
	"encoding/json"

	"github.com/nousresearch/hermes-go/pkg/skill"
)

var skillsListSchema = map[string]any{
	"name":        "skills_list",
	"description": "List all available skills (tier 1 — minimal metadata). Returns only name, brief description, and category to minimize token usage. Use skill_view to load full metadata and linked files.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"category": map[string]any{
				"type":        "string",
				"description": "Optional category filter (e.g. 'research', 'productivity', 'mlops'). If omitted, all skills are listed.",
			},
		},
		"required": []any{},
	},
}

func skillsListHandler(args map[string]any) string {
	category, _ := args["category"].(string)

	skills := skill.ListBrief()

	var filtered []skill.BriefSkill
	if category != "" {
		for _, s := range skills {
			if s.Category == category {
				filtered = append(filtered, s)
			}
		}
	} else {
		filtered = skills
	}

	result := map[string]any{
		"success": true,
		"skills":  filtered,
		"count":   len(filtered),
		"hint":    "Use skill_view(name) to see full metadata, skill_view(name, file_path) for linked files",
	}

	data, err := json.Marshal(result)
	if err != nil {
		return toolError(err.Error())
	}
	return string(data)
}

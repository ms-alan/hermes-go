package tools

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// searchFilesSchema defines the search_files tool.
var searchFilesSchema = map[string]any{
	"name":        "search_files",
	"description": "Search file contents using ripgrep (rg) for fast content search, or find files by name. Uses rg when searching file contents (respects .gitignore, excludes hidden dirs), falls back to find(1) for file-name search. Returns file paths, match counts, or full matching lines.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regex pattern for content search, or glob pattern (e.g. '*.py') for file search.",
			},
			"target": map[string]any{
				"type":        "string",
				"description": "'content' (search inside files, default) or 'files' (find files by name).",
				"enum":        []any{"content", "files"},
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory or file to search in (default: current working directory).",
			},
			"file_glob": map[string]any{
				"type":        "string",
				"description": "Filter files by glob pattern (e.g. '*.py') when target=content.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results (default: 50).",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Skip first N results for pagination (default: 0).",
			},
			"output_mode": map[string]any{
				"type":        "string",
				"description": "'content' (default, shows matching lines with line numbers), 'files_only' (file paths only), or 'count' (match counts per file).",
				"enum":        []any{"content", "files_only", "count"},
			},
			"context": map[string]any{
				"type":        "integer",
				"description": "Number of context lines before and after each match (default: 0).",
			},
		},
		"required": []any{"pattern"},
	},
}

// searchFilesHandler is the tool handler for search_files.
func searchFilesHandler(args map[string]any) string {
	pattern, _ := args["pattern"].(string)
	if pattern == "" {
		return toolError("pattern is required")
	}

	target, _ := args["target"].(string)
	if target == "" {
		target = "content"
	}
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	fileGlob, _ := args["file_glob"].(string)
	limitStr, _ := args["limit"].(string)
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}
	offsetStr, _ := args["offset"].(string)
	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil {
			offset = o
		}
	}
	outputMode, _ := args["output_mode"].(string)
	if outputMode == "" {
		outputMode = "content"
	}
	contextStr, _ := args["context"].(string)
	context := 0
	if contextStr != "" {
		if c, err := strconv.Atoi(contextStr); err == nil {
			context = c
		}
	}

	if target == "files" {
		return searchFilesFind(pattern, path, limit, offset)
	}
	return searchFilesContent(pattern, path, fileGlob, limit, offset, outputMode, context)
}

func searchFilesContent(pattern, path, fileGlob string, limit, offset int, outputMode string, context int) string {
	args := []string{}
	if outputMode == "count" {
		args = append(args, "-c")
	} else if outputMode == "files_only" {
		args = append(args, "-l")
	} else {
		args = append(args, "-n") // line numbers
	}
	if context > 0 {
		args = append(args, "-C", strconv.Itoa(context))
	} else {
		args = append(args, "--max-count", strconv.Itoa(limit))
	}
	if fileGlob != "" {
		args = append(args, "-g", fileGlob)
	}
	// Skip hidden dirs and .git by default (rg default)
	args = append(args, pattern, path)

	cmd := exec.Command("rg", args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return toolError(fmt.Sprintf("ripgrep error: %s", string(ee.Stderr)))
		}
		return toolError(fmt.Sprintf("ripgrep error: %v", err))
	}

	output := string(out)
	if output == "" {
		return toolResultData(map[string]any{
			"pattern": pattern,
			"path":    path,
			"matches": []string{},
			"count":   0,
		})
	}

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	// Apply offset/limit to content results
	if offset > 0 && offset < len(lines) {
		lines = lines[offset:]
	}
	if limit > 0 && limit < len(lines) {
		lines = lines[:limit]
	}

	return toolResultData(map[string]any{
		"pattern": pattern,
		"path":    path,
		"matches": lines,
		"count":   len(lines),
	})
}

func searchFilesFind(pattern, path string, limit, offset int) string {
	// Use ripgrep for file name search if available (faster, respects .gitignore)
	cmd := exec.Command("rg", "-l", "--max-count", strconv.Itoa(limit+offset), "--glob", "!"+".*", pattern, path)
	out, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
		if offset > 0 && offset < len(lines) {
			lines = lines[offset:]
		}
		if limit > 0 && limit < len(lines) {
			lines = lines[:limit]
		}
		return toolResultData(map[string]any{
			"pattern": pattern,
			"path":    path,
			"files":   lines,
			"count":   len(lines),
		})
	}

	// Fallback: find command
	findCmd := exec.Command("find", path, "-name", pattern, "-type", "f")
	out, err = findCmd.Output()
	if err != nil {
		return toolError(fmt.Sprintf("find error: %v", err))
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if offset > 0 && offset < len(lines) {
		lines = lines[offset:]
	}
	if limit > 0 && limit < len(lines) {
		lines = lines[:limit]
	}
	return toolResultData(map[string]any{
		"pattern": pattern,
		"path":    path,
		"files":   lines,
		"count":   len(lines),
	})
}

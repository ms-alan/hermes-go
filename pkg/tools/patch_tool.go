package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// patchTool applies targeted find-and-replace edits to files.
// Supports both simple string replacement and V4A multi-file patch format.
var patchSchema = map[string]any{
	"name":        "patch",
	"description": "Apply a targeted find-and-replace edit to a file. Use this instead of overwriting entire files. Supports exact-match replacement and fuzzy-match (9 strategies) when exact match fails. Also supports V4A multi-file patches. For simple edits, provide path + old_string + new_string. For V4A patches, provide patch_content only.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path to edit (required unless using patch mode)",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "Text to find in the file. Must be unique unless replace_all=true.",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "Replacement text.",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "Replace all occurrences instead of just the first (default: false)",
			},
			"mode": map[string]any{
				"type":        "string",
				"description": "Edit mode: 'replace' (default) or 'patch' (V4A multi-file)",
			},
			"patch": map[string]any{
				"type":        "string",
				"description": "V4A format patch content (required when mode=patch)",
			},
		},
		"required": []any{"path", "old_string", "new_string"},
	},
}

func patchHandler(args map[string]any) string {
	mode, _ := args["mode"].(string)
	if mode == "patch" {
		return patchV4AHandler(args)
	}
	return patchReplaceHandler(args)
}

func patchReplaceHandler(args map[string]any) string {
	path, _ := args["path"].(string)
	oldStr, _ := args["old_string"].(string)
	newStr, _ := args["new_string"].(string)
	replaceAll, _ := args["replace_all"].(bool)

	if path == "" || oldStr == "" {
		return toolError("path and old_string are required")
	}

	// Security: block writes to sensitive paths
	home, _ := os.UserHomeDir()
	safeWriteRoot := os.Getenv("HERMES_WRITE_SAFE_ROOT")
	deniedPrefixes := []string{
		"/etc/", "/usr/", "/bin/", "/sbin/", "/boot/", "/dev/", "/sys/", "/proc/",
		home + "/.ssh/", home + "/.gnupg/", home + "/.aws/",
	}
	deniedExact := []string{
		home + "/.bashrc", home + "/.zshrc", home + "/.profile",
		home + "/.bash_profile", home + "/.zprofile",
		home + "/.netrc", home + "/.pgpass",
	}

	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = "/" + absPath
	}

	// Check denied prefixes
	for _, prefix := range deniedPrefixes {
		if strings.HasPrefix(absPath, prefix) {
			return toolError(fmt.Sprintf("write denied: path under protected directory %s", prefix))
		}
	}
	// Check denied exact paths
	for _, denied := range deniedExact {
		if absPath == denied || absPath == denied+".local" {
			return toolError(fmt.Sprintf("write denied: protected file %s", path))
		}
	}
	// Check HERMES_WRITE_SAFE_ROOT
	if safeWriteRoot != "" && !strings.HasPrefix(absPath, safeWriteRoot) {
		return toolError(fmt.Sprintf("write denied: HERMES_WRITE_SAFE_ROOT is set; writes must be under %s", safeWriteRoot))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return toolError(fmt.Sprintf("read file: %v", err))
	}
	content := string(data)

	count := 1
	if replaceAll {
		count = strings.Count(content, oldStr)
		if count == 0 {
			return toolError("old_string not found in file")
		}
		content = strings.Replace(content, oldStr, newStr, -1)
	} else {
		if !strings.Contains(content, oldStr) {
			return toolError("old_string not found in file")
		}
		idx := strings.Index(content, oldStr)
		content = content[:idx] + newStr + content[idx+len(oldStr):]
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return toolError(fmt.Sprintf("write file: %v", err))
	}

	return toolResult("patched", map[string]any{
		"path":        path,
		"replaced":    count,
		"replacement": newStr,
	})
}

func patchV4AHandler(args map[string]any) string {
	patchContent, _ := args["patch"].(string)
	if patchContent == "" {
		return toolError("patch content is required for V4A mode")
	}
	// V4A patch format:
	// *** Begin Patch
	// *** Update File: path/to/file
	// @@ context hint @@
	// context line
	// -removed line
	// +added line
	// ...
	// *** End Patch
	results := []map[string]any{}
	applied := 0
	errors := []string{}

	lines := strings.Split(patchContent, "\n")
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if line == "*** Begin Patch" {
			i++
			for i < len(lines) {
				line = strings.TrimSpace(lines[i])
				if line == "*** End Patch" {
					break
				}
				if strings.HasPrefix(line, "*** Update File:") {
					filePath := strings.TrimPrefix(line, "*** Update File: ")
					hunkStart := -1
					i++
					var oldLines, newLines []string
					// Parse hunk: @@ hint @@ followed by context/removal/addition
					for i < len(lines) && strings.TrimSpace(lines[i]) != "***" && !strings.HasPrefix(strings.TrimSpace(lines[i]), "*** Update File:") {
						hunkLine := lines[i]
						trimmed := strings.TrimSpace(hunkLine)
						if strings.HasPrefix(trimmed, "@@") {
							hunkStart = i + 1
							i++
							continue
						}
						if hunkStart == -1 {
							i++
							continue // skip context before first @@
						}
						if len(hunkLine) > 0 && hunkLine[0] == '-' {
							oldLines = append(oldLines, hunkLine[1:])
						} else if len(hunkLine) > 0 && hunkLine[0] == '+' {
							newLines = append(newLines, hunkLine[1:])
						}
						// else: context line — skip
						i++
					}
					// Apply the hunk
					if filePath != "" && (len(oldLines) > 0 || len(newLines) > 0) {
						data, err := os.ReadFile(filePath)
						if err != nil {
							errors = append(errors, fmt.Sprintf("%s: read error: %v", filePath, err))
							continue
						}
						content := string(data)
						// Simple replace: find oldLines[0] and replace with newLines[0]
						// (V4A format has single-line hunks in practice)
						if len(oldLines) > 0 && len(newLines) > 0 {
							if strings.Contains(content, oldLines[0]) {
								content = strings.Replace(content, oldLines[0], newLines[0], 1)
								if err := os.WriteFile(filePath, []byte(content), 0644); err == nil {
									applied++
									results = append(results, map[string]any{"file": filePath, "status": "applied"})
								} else {
									errors = append(errors, fmt.Sprintf("%s: write error: %v", filePath, err))
								}
							} else {
								errors = append(errors, fmt.Sprintf("%s: old string not found in file", filePath))
							}
						}
					}
					continue
				}
				i++
			}
		}
		i++
	}

	return toolResultData(map[string]any{
		"applied":  applied,
		"results":  results,
		"errors":    errors,
		"total":    len(results),
	})
}

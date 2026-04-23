package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// executeCodeHandler runs a Python script in a sandboxed environment.
func executeCodeHandler(args map[string]any) string {
	code, _ := args["code"].(string)
	if code == "" {
		return toolError("code is required")
	}

	// Sanitize: disallow obvious escape hatches
	lower := strings.ToLower(code)
	if strings.Contains(lower, "import os") && strings.Contains(lower, "environ") {
		return toolError("sandbox: cannot import os.environ directly — use env_passthrough in config")
	}
	if strings.Contains(lower, "import sys") && strings.Contains(lower, "path") {
		return toolError("sandbox: cannot modify sys.path")
	}
	if strings.Contains(lower, "import subprocess") {
		return toolError("sandbox: subprocess is not allowed")
	}
	if strings.Contains(lower, "__import__") {
		return toolError("sandbox: __import__ is not allowed")
	}

	config := DefaultCodeExecutionConfig()

	// Allow only sandbox-allowed tools to be called
	handler := func(toolName string, toolArgs map[string]any) string {
		allowed := false
		for _, t := range sandboxAllowedTools {
			if t == toolName {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Sprintf(`{"error": "tool %q not allowed in sandbox"}`, toolName)
		}
		// Dispatch via the real tool handlers
		result := Call(toolName, toolArgs)
		// Serialize ToolResult back to JSON for the sandboxed script
		resp := map[string]any{
			"success": result.Success,
			"error":   result.Error,
			"output":  result.Output,
		}
		b, _ := json.Marshal(resp)
		return string(b)
	}

	ctx := context.Background()
	output, err := ExecuteCode(ctx, code, config, handler)
	if err != nil {
		return toolError(fmt.Sprintf("execute failed: %v", err))
	}

	return toolResultData(map[string]any{
		"output": output,
	})
}

// CodeExecutionSchema is the JSON schema for execute_code.
var CodeExecutionSchema = map[string]any{
	"name":        "execute_code",
	"description": "Execute a Python script in a sandboxed environment with access to Hermes tools via RPC. The script can call web_search, web_extract, read_file, write_file, search_files, patch, and terminal. Returns the script's stdout.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"code": map[string]any{
				"type":        "string",
				"description": "The Python script to execute. Can call hermes_tools functions: web_search(query, limit=5), web_extract(urls), read_file(path, offset=1, limit=500), write_file(path, content), search_files(pattern, target='content', path='.'), patch(path, old_string, new_string), terminal(command, timeout=60). Example:\n\n```python\nresults = web_search('Claude AI', limit=3)\nfor r in results.get('data', {}).get('web', []):\n    print(r['title'], r['url'])\n\ncontent = read_file('/tmp/example.txt')\nprint(content['content'][:200])\n```",
			},
		},
		"required": []any{"code"},
	},
}

package tools

import (
	"os"
	"strings"
)

var envPassthroughSchema = map[string]any{
	"name":        "env_passthrough",
	"description": "Read environment variables from the agent's process. Returns all variables or a filtered subset.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"filter": map[string]any{
				"type":        "string",
				"description": "Optional prefix filter — only vars starting with this prefix are returned",
			},
			"mask": map[string]any{
				"type":        "boolean",
				"description": "If true, mask the values of sensitive variables (default: true)",
			},
		},
	},
}

func envPassthroughHandler(args map[string]any) string {
	filter, _ := args["filter"].(string)
	mask, _ := args["mask"].(bool)
	if !mask {
		mask = true // default to true
	}

	vars := map[string]string{}

	if filter != "" {
		// Return only vars starting with filter prefix
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, filter) {
				pair := strings.SplitN(kv, "=", 2)
				if len(pair) == 2 {
					key := pair[0]
					val := pair[1]
					if mask && isSensitive(key) {
						vars[key] = "***"
					} else {
						vars[key] = val
					}
				}
			}
		}
	} else {
		// Return all vars (sensitive ones masked)
		for _, kv := range os.Environ() {
			pair := strings.SplitN(kv, "=", 2)
			if len(pair) == 2 {
				key := pair[0]
				val := pair[1]
				if mask && isSensitive(key) {
					vars[key] = "***"
				} else {
					vars[key] = val
				}
			}
		}
	}

	return toolResultData(vars)
}

func isSensitive(key string) bool {
	upper := strings.ToUpper(key)
	sensitive := []string{
		"API", "KEY", "TOKEN", "SECRET", "PASSWORD", "PASS", "CREDENTIAL",
		"AUTH", "PRIVATE", "JWT", "BEARER",
	}
	for _, s := range sensitive {
		if strings.Contains(upper, s) {
			return true
		}
	}
	return false
}

// Package tools builtin provides the core built-in tools for hermes-go:
//
//   - file_read:  read a file with optional line offset and limit
//   - file_write: write content to a file (overwrites existing)
//   - terminal:   execute a shell command and return its stdout/stderr
//   - web_search: search the web using Tavily API (env: TAVILY_API_KEY)
//
// All tools self-register by calling tools.Register at package init time.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Tool schemas
// ---------------------------------------------------------------------------

var fileReadSchema = map[string]any{
	"name":        "file_read",
	"description": "Read a file with optional line offset and limit. Returns file content with line numbers. Use offset=1 and limit=500 for typical reads.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to the file to read",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "1-indexed starting line number (default: 1)",
				"default":     1,
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of lines to read (default: 500, max: 2000)",
				"default":     500,
			},
		},
		"required": []any{"path"},
	},
}

var fileWriteSchema = map[string]any{
	"name":        "file_write",
	"description": "Write content to a file. Creates the file (and parent directories) if it does not exist, or overwrites existing content.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute or relative path to the file to write",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The content to write to the file",
			},
		},
		"required": []any{"path", "content"},
	},
}

var browserNavigateSchema = map[string]any{
	"name":        "browser_navigate",
	"description": "Navigate to a URL and get a text summary of the page.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "URL to navigate to (http/https)",
			},
		},
		"required": []any{"url"},
	},
}

var browserSnapshotSchema = map[string]any{
	"name":        "browser_snapshot",
	"description": "Get a text snapshot of the current browser page.",
	"parameters": map[string]any{
		"type": "object",
		"properties":  map[string]any{},
	},
}

var browserScreenshotSchema = map[string]any{
	"name":        "browser_screenshot",
	"description": "Take a screenshot of the current page and save to a file.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Optional path to save the screenshot (default: ~/Downloads/hermes_screenshot.png)",
			},
			"question": map[string]any{
				"type":        "string",
				"description": "Optional question about the screenshot (for AI analysis via vision_analyze)",
			},
		},
	},
}

var browserClickSchema = map[string]any{
	"name":        "browser_click",
	"description": "Click an element on the current page.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref": map[string]any{
				"type":        "string",
				"description": "Element reference from browser_snapshot (e.g. @e5)",
			},
			"selector": map[string]any{
				"type":        "string",
				"description": "CSS selector (alternative to ref)",
			},
		},
		"required": []any{},
	},
}

var browserTypeSchema = map[string]any{
	"name":        "browser_type",
	"description": "Type text into an input field.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref": map[string]any{
				"type":        "string",
				"description": "Element reference from browser_snapshot (e.g. @e3)",
			},
			"selector": map[string]any{
				"type":        "string",
				"description": "CSS selector (alternative to ref)",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "Text to type",
			},
		},
		"required": []any{"text"},
	},
}

var browserBackSchema = map[string]any{
	"name":        "browser_back",
	"description": "Navigate back to the previous page.",
	"parameters": map[string]any{
		"type": "object",
		"properties":  map[string]any{},
	},
}

var browserScrollSchema = map[string]any{
	"name":        "browser_scroll",
	"description": "Scroll the current page up or down.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"direction": map[string]any{
				"type":        "string",
				"enum":        []any{"up", "down"},
				"description": "Direction to scroll (up or down)",
			},
		},
		"required": []any{"direction"},
	},
}

var browserPressSchema = map[string]any{
	"name":        "browser_press",
	"description": "Press a keyboard key on the current page.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key": map[string]any{
				"type":        "string",
				"description": "Key to press (Enter, Tab, Escape, ArrowUp, ArrowDown, etc.)",
			},
		},
		"required": []any{"key"},
	},
}

var processSchema = map[string]any{
	"name":        "process",
	"description": "Manage background processes — list, get, register, unregister.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action: list, get, register, unregister",
				"enum":        []any{"list", "get", "register", "unregister"},
			},
			"id": map[string]any{
				"type":        "string",
				"description": "Process ID (for get/unregister/register)",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "Command string (for register)",
			},
			"sessionId": map[string]any{
				"type":        "string",
				"description": "Optional session ID to associate with the process",
			},
		},
		"required": []any{"action"},
	},
}

var terminalSchema = map[string]any{
	"name":        "terminal",
	"description": "Execute a shell command in the terminal and return stdout and stderr output. The command runs in the process's current working directory unless cwd is provided.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute (e.g. 'ls -la' or 'go build ./...')",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Timeout in seconds (default: 60, max: 600)",
				"default":     60,
			},
			"cwd": map[string]any{
				"type":        "string",
				"description": "Working directory for the command (default: current directory)",
			},
		},
		"required": []any{"command"},
	},
}

var memorySchema = map[string]any{
	"name":        "memory",
	"description": "Manage agent persistent memory (MEMORY.md and USER.md). Actions: add, replace, remove, snapshot, freeze, show.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []any{"add", "replace", "remove", "snapshot", "freeze", "show"}},
			"target": map[string]any{"type": "string", "enum": []any{"memory", "user"}, "default": "memory"},
			"content":   map[string]any{"type": "string", "description": "Content for add/replace"},
			"old_text":  map[string]any{"type": "string", "description": "Exact substring to replace or remove"},
			"depth":     map[string]any{"type": "number", "description": "Snapshot depth (1-3, default 1)"},
		},
		"required": []any{"action"},
	},
}

var cronSchema = map[string]any{
	"name":        "cronjob",
	"description": "Manage scheduled cron jobs — create, list, get, remove, pause, resume, or run a job immediately.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":   map[string]any{"type": "string", "enum": []any{"create", "list", "get", "remove", "pause", "resume", "run"}},
			"id":       map[string]any{"type": "string", "description": "Job ID (for get/remove/pause/resume/run)"},
			"prompt":   map[string]any{"type": "string", "description": "Task prompt (for create)"},
			"schedule": map[string]any{"type": "string", "description": "Schedule: 30m, every 2h, 0 9 * * *, 2026-02-03T14:00 (for create)"},
			"name":     map[string]any{"type": "string", "description": "Friendly job name (for create)"},
			"deliver":  map[string]any{"type": "string", "description": "Delivery: origin, local, or platform:chat_id (for create)"},
			"repeat":    map[string]any{"type": "number", "description": "Max repeat count (for create, nil=forever)"},
			"skills":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Skills to load (for create)"},
			"enabled":   map[string]any{"type": "boolean", "description": "Filter by enabled state (for list)"},
			"state":     map[string]any{"type": "string", "description": "Filter by state (for list)"},
		},
		"required": []any{"action"},
	},
}

var webSearchSchema = map[string]any{
	"name":        "web_search",
	"description": "Search the web for information using the Tavily API. Returns a list of relevant web results with titles, URLs, and descriptions. Set TAVILY_API_KEY in environment to enable.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query to look up on the web",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results to return (default: 5)",
				"default":     5,
			},
		},
		"required": []any{"query"},
	},
}

// ---------------------------------------------------------------------------
// Blocked / sensitive paths
// ---------------------------------------------------------------------------

var blockedDevicePaths = map[string]bool{
	"/dev/zero":   true,
	"/dev/random":  true,
	"/dev/urandom": true,
	"/dev/full":    true,
	"/dev/stdin":   true,
	"/dev/tty":     true,
	"/dev/console": true,
	"/dev/stdout":  true,
	"/dev/stderr":  true,
}

var sensitivePathPrefixes = []string{
	"/etc/",
	"/boot/",
	"/usr/lib/systemd/",
	"/private/etc/",
	"/private/var/",
}

func isBlockedDevice(path string) bool {
	normalized := filepath.Clean(os.ExpandEnv(path))
	if blockedDevicePaths[normalized] {
		return true
	}
	// /proc/self/fd/0-2 and /proc/<pid>/fd/0-2 are Linux aliases for stdio.
	if strings.HasPrefix(normalized, "/proc/") {
		for _, suffix := range []string{"/fd/0", "/fd/1", "/fd/2"} {
			if strings.HasSuffix(normalized, suffix) {
				return true
			}
		}
	}
	return false
}

func isSensitivePath(path string) bool {
	normalized := filepath.Clean(os.ExpandEnv(path))
	for _, prefix := range sensitivePathPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func fileReadHandler(args map[string]any) string {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return toolError("file_read requires a 'path' argument")
	}

	if isBlockedDevice(path) {
		return toolError(fmt.Sprintf("cannot read device path %q: would block or produce infinite output", path))
	}

	offset := 1
	if o, ok := args["offset"].(float64); ok {
		offset = int(o)
	}
	if offset < 1 {
		offset = 1
	}

	limit := 500
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}
	if limit < 1 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return toolError(fmt.Sprintf("invalid path: %v", err))
	}

	fileInfo, err := os.Stat(absPath)
	if err != nil {
		return toolError(fmt.Sprintf("file not found or unreadable: %s", path))
	}
	if fileInfo.IsDir() {
		return toolError(fmt.Sprintf("path %q is a directory, not a file", path))
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return toolError(fmt.Sprintf("failed to read file: %v", err))
	}

	lines := strings.Split(string(content), "\n")
	totalLines := len(lines)

	// Clamp offset to valid range.
	if offset < 1 {
		offset = 1
	}
	if offset > totalLines {
		return toolResultData(map[string]any{
			"path":        path,
			"content":     "",
			"offset":      offset,
			"limit":       limit,
			"total_lines": totalLines,
			"truncated":   false,
		})
	}

	end := offset + limit
	if end > totalLines+1 {
		end = totalLines + 1
	}

	selected := lines[offset-1 : end-1]
	var buf bytes.Buffer
	for i, line := range selected {
		buf.WriteString(fmt.Sprintf("%d|%s\n", offset+i, line))
	}
	resultContent := buf.String()
	if end-1 < totalLines {
		resultContent += fmt.Sprintf("%d|... (%d more lines)\n", totalLines+1, totalLines-end+1)
	}

	return toolResultData(map[string]any{
		"path":        path,
		"content":     strings.TrimSuffix(resultContent, "\n"),
		"offset":      offset,
		"limit":       limit,
		"total_lines": totalLines,
		"truncated":   end-1 < totalLines,
	})
}

func fileWriteHandler(args map[string]any) string {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return toolError("file_write requires a 'path' argument")
	}

	content, ok := args["content"].(string)
	if !ok {
		return toolError("file_write requires a 'content' argument")
	}

	// Authorization check (approval.go) — scans path for dangerous targets.
	if approved, reason := Authorize("file_write", path, ""); !approved {
		return toolError(fmt.Sprintf("file_write blocked: %s", reason))
	}

	if isBlockedDevice(path) {
		return toolError(fmt.Sprintf("refusing to write to blocked device path %q", path))
	}
	if isSensitivePath(path) {
		return toolError(fmt.Sprintf("refusing to write to sensitive system path: %s\nUse the terminal tool with sudo if you need to modify system files.", path))
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return toolError(fmt.Sprintf("invalid path: %v", err))
	}

	// Create parent directories if they don't exist.
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return toolError(fmt.Sprintf("failed to create parent directory: %v", err))
	}

	// Check if file exists and warn about overwriting.
	fileInfo, err := os.Stat(absPath)
	if err == nil && !fileInfo.IsDir() {
		// File exists and is a regular file — allow overwrite.
	}

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return toolError(fmt.Sprintf("failed to write file: %v", err))
	}

	bytesWritten := int64(len(content))
	return toolResultData(map[string]any{
		"path":         path,
		"bytes_written": bytesWritten,
		"success":      true,
	})
}

// ---------------------------------------------------------------------------
// file_delete
// ---------------------------------------------------------------------------

var fileDeleteSchema = map[string]any{
	"name":        "file_delete",
	"description": "Delete a file or empty directory. Use with caution — deleted files cannot be recovered.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute path of the file or directory to delete",
			},
			"recursive": map[string]any{
				"type":        "boolean",
				"description": "If true, delete directory and all its contents",
			},
		},
		"required": []any{"path"},
	},
}

func fileDeleteHandler(args map[string]any) string {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return toolError("file_delete requires a 'path' argument")
	}

	// Authorization check (approval.go) — scans path for sensitive targets.
	if approved, reason := Authorize("file_delete", path, ""); !approved {
		return toolError(fmt.Sprintf("file_delete blocked: %s", reason))
	}

	absPath, err := filepath.Abs(os.ExpandEnv(path))
	if err != nil {
		return toolError(fmt.Sprintf("invalid path: %v", err))
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return toolError(fmt.Sprintf("path does not exist: %s", absPath))
		}
		return toolError(fmt.Sprintf("stat: %v", err))
	}

	recursive, _ := args["recursive"].(bool)
	if info.IsDir() && !recursive {
		return toolError(fmt.Sprintf("path is a directory (use recursive:true to delete anyway): %s", absPath))
	}

	if info.IsDir() {
		if err := os.RemoveAll(absPath); err != nil {
			return toolError(fmt.Sprintf("failed to delete directory: %v", err))
		}
	} else {
		if err := os.Remove(absPath); err != nil {
			return toolError(fmt.Sprintf("failed to delete file: %v", err))
		}
	}

	return toolResultData(map[string]any{
		"path":      absPath,
		"type":      map[bool]string{true: "directory", false: "file"}[info.IsDir()],
		"recursive": recursive,
		"success":   true,
	})
}

func terminalHandler(args map[string]any) string {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return toolError("terminal requires a 'command' argument")
	}

	// Authorization check (approval.go) — scans command for dangerous patterns.
	if approved, reason := Authorize("terminal", command, ""); !approved {
		return toolError(fmt.Sprintf("terminal command blocked: %s", reason))
	}

	timeout := 60
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}
	if timeout < 1 {
		timeout = 1
	}
	if timeout > 600 {
		timeout = 600
	}

	cwd := ""
	if w, ok := args["cwd"].(string); ok {
		cwd = w
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if strings.Contains(command, "&&") || strings.Contains(command, "||") || strings.Contains(command, ";") {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	} else {
		parts := strings.Fields(command)
		if len(parts) == 0 {
			return toolError("empty command")
		}
		cmd = exec.CommandContext(ctx, parts[0], parts[1:]...)
	}

	cmd.Env = os.Environ()
	if cwd != "" {
		cmd.Dir = cwd
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := stdout.String()
	if err != nil {
		output += stderr.String()
	}

	// Check for context deadline exceeded.
	if ctx.Err() == context.DeadlineExceeded {
		return toolError(fmt.Sprintf("command timed out after %d seconds", timeout))
	}

	if err != nil {
		exitCode := -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		return toolResultData(map[string]any{
			"command":  command,
			"stdout":   stdout.String(),
			"stderr":   stderr.String(),
			"exitCode": exitCode,
			"error":    err.Error(),
			"success":  false,
		})
	}

	return toolResultData(map[string]any{
		"command":  command,
		"stdout":   stdout.String(),
		"stderr":   stderr.String(),
		"exitCode": 0,
		"success":  true,
	})
}

func webSearchHandler(args map[string]any) string {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return toolError("web_search requires a 'query' argument")
	}

	limit := 5
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 20 {
		limit = 20
	}

	apiKey := os.Getenv("TAVILY_API_KEY")
	if apiKey == "" {
		return toolResultData(map[string]any{
			"query": query,
			"results": []map[string]any{
				{
					"title":       "Web search not configured",
					"url":         "",
					"description": fmt.Sprintf("TAVILY_API_KEY is not set. Set it in ~/.hermes/.env to enable web search. Get your key at https://tavily.com"),
				},
			},
			"info": "Set TAVILY_API_KEY in environment to enable web search",
		})
	}

	// Build Tavily request
	tavilyReq := map[string]any{
		"query":         query,
		"api_key":       apiKey,
		"max_results":   limit,
		"search_depth":  "basic",
		"include_answer": false,
	}
	reqBody, err := json.Marshal(tavilyReq)
	if err != nil {
		return toolError(fmt.Sprintf("failed to marshal request: %v", err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.tavily.com/search", bytes.NewReader(reqBody))
	if err != nil {
		return toolError(fmt.Sprintf("failed to create request: %v", err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return toolError(fmt.Sprintf("Tavily API request failed: %v", err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return toolError(fmt.Sprintf("failed to read response: %v", err))
	}

	if resp.StatusCode != http.StatusOK {
		return toolError(fmt.Sprintf("Tavily API returned status %d: %s", resp.StatusCode, string(body)))
	}

	var tavilyResp struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Content     string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &tavilyResp); err != nil {
		return toolError(fmt.Sprintf("failed to parse Tavily response: %v", err))
	}

	results := make([]map[string]any, 0, len(tavilyResp.Results))
	for i, r := range tavilyResp.Results {
		desc := r.Description
		if desc == "" {
			desc = r.Content
		}
		// Truncate long descriptions to save tokens
		if len(desc) > 300 {
			desc = desc[:300] + "..."
		}
		results = append(results, map[string]any{
			"position":    i + 1,
			"title":       r.Title,
			"url":         r.URL,
			"description": desc,
		})
	}

	return toolResultData(map[string]any{
		"query":   query,
		"results": results,
		"count":   len(results),
	})
}

// ---------------------------------------------------------------------------
// Availability checks
// ---------------------------------------------------------------------------

// checkTerminalEnv verifies the terminal environment is usable.
func checkTerminalEnv() bool {
	_, err := exec.LookPath("sh")
	return err == nil
}

// checkFileTools verifies the filesystem is accessible.
func checkFileTools() bool {
	tmpDir := os.TempDir()
	f, err := os.Create(filepath.Join(tmpDir, ".hermes_go_filetest"))
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// checkWebSearch verifies the Tavily API key is configured.
func checkWebSearch() bool {
	return os.Getenv("TAVILY_API_KEY") != ""
}

// ---------------------------------------------------------------------------
// Package init — self-register all built-in tools
// ---------------------------------------------------------------------------

func init() {
	Register(
		"file_read",
		"builtin",
		fileReadSchema,
		fileReadHandler,
		checkFileTools,
		nil,
		false,
		"Read a file with optional line offset and limit",
		"📄",
	)

	Register(
		"file_write",
		"builtin",
		fileWriteSchema,
		fileWriteHandler,
		checkFileTools,
		nil,
		false,
		"Write content to a file, creating or overwriting as needed",
		"✏️",
	)

	Register(
		"file_delete",
		"builtin",
		fileDeleteSchema,
		fileDeleteHandler,
		checkFileTools,
		nil,
		false,
		"Delete a file or empty directory (use recursive:true for directories)",
		"🗑️",
	)

	Register(
		"terminal",
		"builtin",
		terminalSchema,
		terminalHandler,
		checkTerminalEnv,
		nil,
		false,
		"Execute a shell command and return its output",
		"💻",
	)

	Register(
		"process",
		"builtin",
		processSchema,
		processToolHandler,
		nil,
		nil,
		false,
		"Manage background processes — list, get, register, unregister",
		"⚙️",
	)

	Register(
		"web_search",
		"builtin",
		webSearchSchema,
		webSearchHandler,
		checkWebSearch,
		nil,
		false,
		"Search the web using Tavily API (requires TAVILY_API_KEY)",
		"🔍",
	)

	Register(
		"memory",
		"builtin",
		memorySchema,
		memoryToolHandler,
		nil,
		nil,
		false,
		"Manage agent memory — add/replace/remove/show entries in MEMORY.md or USER.md",
		"🧠",
	)

	Register(
		"cronjob",
		"builtin",
		cronSchema,
		cronToolHandler,
		nil,
		nil,
		false,
		"Manage scheduled cron jobs — create, list, remove, pause, resume, run",
		"⏰",
	)

	Register(
		"browser_navigate",
		"builtin",
		browserNavigateSchema,
		browserNavigateHandler,
		nil,
		nil,
		false,
		"Open a URL in a headless Chrome browser and get a text summary",
		"🌐",
	)

	Register(
		"browser_snapshot",
		"builtin",
		browserSnapshotSchema,
		browserSnapshotHandler,
		nil,
		nil,
		false,
		"Get a text snapshot of the current browser page",
		"📄",
	)

	Register(
		"browser_screenshot",
		"builtin",
		browserScreenshotSchema,
		browserVisionHandler,
		nil,
		nil,
		false,
		"Take a screenshot of the current browser page and save to file",
		"📸",
	)

	Register(
		"browser_click",
		"builtin",
		browserClickSchema,
		browserClickHandler,
		nil,
		nil,
		false,
		"Click an element on the current page by selector or ref",
		"👆",
	)

	Register(
		"browser_type",
		"builtin",
		browserTypeSchema,
		browserTypeHandler,
		nil,
		nil,
		false,
		"Type text into an input field identified by selector or ref",
		"⌨️",
	)

	Register(
		"browser_back",
		"builtin",
		browserBackSchema,
		browserBackHandler,
		nil,
		nil,
		false,
		"Navigate back to the previous page",
		"⬅️",
	)

	Register(
		"browser_scroll",
		"builtin",
		browserScrollSchema,
		browserScrollHandler,
		nil,
		nil,
		false,
		"Scroll the current page up or down",
		"📜",
	)

	Register(
		"browser_press",
		"builtin",
		browserPressSchema,
		browserPressHandler,
		nil,
		nil,
		false,
		"Press a keyboard key (Enter, Tab, Escape, ArrowUp, etc.)",
		"⌨️",
	)
}

// ---------------------------------------------------------------------------
// Surrogate main for testing
// ---------------------------------------------------------------------------

// This file is not a main package; it is imported by tests via the tools package.
// The _ = toolError / toolResult / toolResultData references below silence
// unused-function warnings in environments that import this file directly.
var _ = strconv.Itoa
var _ = json.Marshal // referenced via toolResult
var _ = []any{}

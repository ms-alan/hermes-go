// Package tools provides the dangerous-command authorization system.
//
// Architecture:
//   - DANGEROUS_PATTERNS: ordered list of (regex, reason) pairs checked before
//     any tool that may write or execute.
//   - ApprovalState: per-session-key map[toolName]bool, thread-safe.
//   - CheckDangerous(cmd string) (bool, reason): scans a shell command string.
//   - CheckFileDangerous(path string) (bool, reason): scans a file path.
//
// Integration: call Authorize(toolName, args, sessionKey) at the top of each
// dangerous tool's Run() method. It returns (approved bool, reason string).
// An empty reason means the command was auto-approved.
package tools

import (
	"os"
	"regexp"
	"strings"
	"sync"
)

// ============================================================================
// Dangerous pattern registry
// ============================================================================

// DANGEROUS_PATTERNS are checked in order; the first match determines the reason.
var DANGEROUS_PATTERNS = []struct {
	regex   *regexp.Regexp
	pattern string // raw pattern for display
	reason  string // human-readable reason
}{
	// Recursive or root-level deletions
	{regexp.MustCompile(`\brm\s+(-[^\s]*\s*)*(/|~)`), `\brm\s+(-[^\s]*\s*)*/`, "delete at root path"},
	{regexp.MustCompile(`\brm\s+-[rfv]+\s+`), `\brm\s+-[rfv]+`, "recursive delete (rm -rf)"},
	{regexp.MustCompile(`\brm\s+--recursive\b`), `\brm\s+--recursive`, "recursive delete (--recursive)"},
	{regexp.MustCompile(`\brm\s+--force\b`), `\brm\s+--force`, "force delete (--force)"},
	{regexp.MustCompile(`\bdel\s+([/\\]|[a-zA-Z]:\\)`), `\bdel\s+[/\\]`, "delete at root path"},

	// Dangerous chmod
	{regexp.MustCompile(`\bchmod\s+(-[^\s]*\s*)*(777|000|666)\b`), `\bchmod\s+.*777|666`, "world-writable permissions"},

	// Dangerous git
	{regexp.MustCompile(`\bgit\s+push\s+.*--force\b`), `\bgit\s+push\s+.*--force`, "force push (overwrites remote history)"},
	{regexp.MustCompile(`\bgit\s+push\s+.*force-delete\b`), `\bgit\s+push\s+.*force-delete`, "force-delete remote branches"},

	// Dangerous docker
	{regexp.MustCompile(`\bdocker\s+run\s+.*--privileged\b`), `\bdocker\s+run\s+.*--privileged`, "privileged container (full host access)"},
	{regexp.MustCompile(`\bdocker\s+run\s+.*--net=host\b`), `\bdocker\s+run\s+.*--net=host`, "host networking (bypasses isolation)"},
	{regexp.MustCompile(`\bdocker\s+rmi\s+`), `\bdocker\s+rmi`, "docker image deletion"},
	{regexp.MustCompile(`\bdocker\s+system\s+prune\b`), `\bdocker\s+system\s+prune`, "docker system prune (deletes all images/volumes)"},
	{regexp.MustCompile(`\bdocker\s+kill\b`), `\bdocker\s+kill`, "kill running containers"},

	// Fork bombs and resource exhaustion
	// Fork bombs — match bash function definition + fork call
	{regexp.MustCompile(`\B:\s*\{\s*:\|:&\s*\}\s*;`), `\B:\s*\{\s*:\|:&\s*\}\s*;`, "fork bomb detected"},
	{regexp.MustCompile(`\bkill\s+-9\s+-1\b`), `\bkill\s+-9\s+-1`, "kill all processes (kill -9 -1)"},

	// Sensitive paths (file writes/deletes)
	{regexp.MustCompile(`(?i)(/.ssh/|/etc/passwd|/etc/shadow|\.ssh/authorized_keys)`), `/.ssh/|/etc/passwd`, "sensitive system file"},
	{regexp.MustCompile(`(?i)(~\$\.env|/\.env|/\.hermes/\.env)`), `~\.env or /\.hermes/\.env`, "credentials file"},
}

// DANGEROUS_PATH_PATTERNS check file paths for dangerous targets.
var DANGEROUS_PATH_PATTERNS = []struct {
	regex  *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`(?i)^/etc/`), "system directory /etc"},
	{regexp.MustCompile(`(?i)^/dev/`), "device file /dev"},
	{regexp.MustCompile(`(?i)\.ssh/authorized_keys?$`), "SSH authorized_keys"},
	{regexp.MustCompile(`(?i)\.ssh/id_[rd]sa$`), "SSH private key"},
	{regexp.MustCompile(`(?i)\.env$`), ".env credentials file"},
	{regexp.MustCompile(`(?i)(^|[/\\])\.hermes([/\\]\.env)?$`), ".hermes config directory"},
	{regexp.MustCompile(`(?i)^/sys/`), "sysfs directory"},
}

// CheckDangerous returns (isDangerous, reason). Empty reason means safe.
func CheckDangerous(cmd string) (bool, string) {
	for _, p := range DANGEROUS_PATTERNS {
		if p.regex.MatchString(cmd) {
			return true, p.reason
		}
	}
	return false, ""
}

// CheckFileDangerous returns (isDangerous, reason) for a file path.
func CheckFileDangerous(path string) (bool, string) {
	// Resolve ~ and common env var prefixes
	expanded := os.ExpandEnv(path)
	expanded = strings.ReplaceAll(expanded, "~", os.Getenv("HOME"))

	for _, p := range DANGEROUS_PATH_PATTERNS {
		if p.regex.MatchString(expanded) {
			return true, p.reason
		}
	}
	return false, ""
}

// ============================================================================
// Per-session approval state
// ============================================================================

// ApprovalState holds the per-session dangerous-tool approval map.
// It is safe for concurrent use.
type ApprovalState struct {
	mu        sync.RWMutex
	permanent map[string]bool // toolName → always allowed (loaded from config)
	sessions  map[string]map[string]bool // sessionKey → toolName → approved
}

var approvalState = &ApprovalState{
	permanent: make(map[string]bool),
	sessions:  make(map[string]map[string]bool),
}

// RegisterPermanentApproval marks a tool as permanently approved (from allowlist).
func RegisterPermanentApproval(toolName string) {
	approvalState.mu.Lock()
	defer approvalState.mu.Unlock()
	approvalState.permanent[toolName] = true
}

// IsApproved returns true if the tool is approved for this session.
// Thread-safe.
func (s *ApprovalState) IsApproved(sessionKey, toolName string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.permanent[toolName] {
		return true
	}
	if sessionKey == "" {
		sessionKey = "default"
	}
	return s.sessions[sessionKey][toolName]
}

// Approve marks the tool as approved for the given session.
// Thread-safe.
func (s *ApprovalState) Approve(sessionKey, toolName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionKey == "" {
		sessionKey = "default"
	}
	if s.sessions[sessionKey] == nil {
		s.sessions[sessionKey] = make(map[string]bool)
	}
	s.sessions[sessionKey][toolName] = true
}

// Deny revokes approval for the tool in this session.
// Thread-safe.
func (s *ApprovalState) Deny(sessionKey, toolName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionKey == "" {
		sessionKey = "default"
	}
	if s.sessions[sessionKey] == nil {
		return
	}
	delete(s.sessions[sessionKey], toolName)
}

// ClearSession removes all approval state for a session.
// Thread-safe.
func (s *ApprovalState) ClearSession(sessionKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionKey)
}

// ============================================================================
// Authorization middleware
// ============================================================================

// DangerousTools is the set of tools that require authorization checks.
var DangerousTools = map[string]bool{
	"file_write":   true,
	"file_delete":  true,
	"bash":         true,
	"exec":         true,
	"mcp_*":        true, // wildcard: any MCP tool that might exec
	"delegate_task": false, // delegate_task is blocked in delegate_tool.go itself
}

// Authorize checks whether a tool may execute for the given session.
// Returns (approved bool, reason string).
// If the tool is not in DangerousTools, returns (true, "").
// If permanently approved, returns (true, "allowlisted").
// If the command matches a dangerous pattern, returns (false, reason).
// Otherwise returns (true, "") — caller should prompt the user if reason is
// non-empty and approved is false.
func Authorize(toolName, args, sessionKey string) (bool, string) {
	// Wildcard check for MCP tools
	isMCP := strings.HasPrefix(toolName, "mcp_")
	isDangerous := DangerousTools[toolName] || (isMCP && DangerousTools["mcp_*"])

	if !isDangerous {
		return true, ""
	}

	// Permanent allowlist check
	if approvalState.IsApproved(sessionKey, toolName) {
		return true, "allowlisted"
	}

	// Check for dangerous patterns in the args
	if toolName == "bash" || toolName == "exec" {
		if ok, reason := CheckDangerous(args); ok {
			return false, reason
		}
	}

	if toolName == "file_write" || toolName == "file_delete" {
		// Extract first path-like argument
		path := extractPath(args)
		if path != "" {
			if ok, reason := CheckFileDangerous(path); ok {
				return false, reason
			}
		}
	}

	// Not auto-approved — caller must prompt user
	return false, ""
}

// extractPath pulls the first quoted or whitespace-separated path from args.
func extractPath(args string) string {
	args = strings.TrimSpace(args)
	// Try quoted first
	if len(args) > 0 && (args[0] == '"' || args[0] == '\'') {
		for i := 1; i < len(args); i++ {
			if args[i] == args[0] {
				return args[1:i]
			}
		}
	}
	// Fall back to first word
	parts := strings.Fields(args)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// AuthorizationResult is returned by AuthorizeWithPrompt.
type AuthorizationResult int

const (
	AuthApproved AuthorizationResult = iota
	AuthDenied
	AuthPending // requires user input
)

// AuthorizeWithPrompt combines Authorize check with user prompting in REPL.
// For non-interactive contexts (gateway, cron), set interactive=false and
// AuthPending will be returned instead of blocking.
func AuthorizeWithPrompt(toolName, args, sessionKey string, interactive bool) (AuthorizationResult, string) {
	approved, reason := Authorize(toolName, args, sessionKey)
	if approved {
		return AuthApproved, ""
	}
	if !interactive {
		return AuthPending, reason
	}
	// TODO: integrate with REPL for interactive yes/no prompt
	// For now, fall back to non-interactive deny
	return AuthDenied, reason
}

// String returns a human-readable name for AuthorizationResult.
func (r AuthorizationResult) String() string {
	switch r {
	case AuthApproved:
		return "approved"
	case AuthDenied:
		return "denied"
	case AuthPending:
		return "pending"
	default:
		return "unknown"
	}
}

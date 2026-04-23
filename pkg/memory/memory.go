// Package memory provides persistent curated memory for hermes-go.
//
// Two stores:
//   - MEMORY.md: agent's personal notes and observations (environment facts,
//     project conventions, tool quirks, things learned)
//   - USER.md: what the agent knows about the user (preferences,
//     communication style, expectations, workflow habits)
//
// Both are injected into the system prompt as a frozen snapshot at session
// start. Mid-session writes update files on disk immediately (durable) but do
// NOT change the system prompt -- this preserves the prefix cache for the
// entire session. The snapshot refreshes on the next session start.
//
// Entry delimiter: § (section sign). Entries can be multiline.
// Character limits (not tokens): MEMORY.md 2200, USER.md 1375.
//
// Design:
//   - Single memory tool with action parameter: add, replace, remove, read
//   - replace/remove use short unique substring matching
//   - Frozen snapshot pattern: system prompt is stable, tool responses show live state
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	// MemoryDir is the subdirectory under HERMES_HOME for memory files.
	MemoryDir = "memories"
	// MemoryFile is the agent's personal notes.
	MemoryFile = "MEMORY.md"
	// UserFile is the user profile.
	UserFile = "USER.md"
	// EntryDelimiter separates entries within a file.
	EntryDelimiter = "\n§\n"

	// DefaultMemoryCharLimit is the max total chars for MEMORY.md.
	DefaultMemoryCharLimit = 2200
	// DefaultUserCharLimit is the max total chars for USER.md.
	DefaultUserCharLimit = 1375

	// Separator is the line used above/below block headers.
	Separator = "═"
)

// MemoryStore manages bounded curated memory with file persistence.
type MemoryStore struct {
	mu              sync.RWMutex
	memoryEntries   []string
	userEntries     []string
	memDir          string
	memCharLimit    int
	userCharLimit   int

	// frozenSnapshot is captured at load time and used for system prompt injection.
	// Mid-session writes update files but NOT this snapshot.
	frozenSnapshot map[string]string // "memory" -> block, "user" -> block

	// Threat patterns for injection detection
	threatPatterns []*threatPattern
}

type threatPattern struct {
	regex   *regexp.Regexp
	patternID string
}

// NewMemoryStore creates a memory store with the default memory directory.
func NewMemoryStore() *MemoryStore {
	home, _ := os.UserHomeDir()
	memDir := filepath.Join(home, ".hermes", MemoryDir)
	return NewMemoryStoreWithDir(memDir)
}

// NewMemoryStoreWithDir creates a memory store with a custom directory.
func NewMemoryStoreWithDir(memDir string) *MemoryStore {
	ms := &MemoryStore{
		memDir:          memDir,
		memCharLimit:    DefaultMemoryCharLimit,
		userCharLimit:   DefaultUserCharLimit,
		frozenSnapshot:  map[string]string{"memory": "", "user": ""},
		threatPatterns:   buildThreatPatterns(),
	}
	return ms
}

// SetCharLimits sets the character limits for memory and user stores.
func (ms *MemoryStore) SetCharLimits(memLimit, userLimit int) {
	ms.memCharLimit = memLimit
	ms.userCharLimit = userLimit
}

// Load reads entries from MEMORY.md and USER.md and captures the frozen snapshot.
func (ms *MemoryStore) Load() error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if err := os.MkdirAll(ms.memDir, 0755); err != nil {
		return fmt.Errorf("memory: create dir: %w", err)
	}

	ms.memoryEntries = ms.readFile(MemoryFile)
	ms.userEntries = ms.readFile(UserFile)

	// Deduplicate (preserves order, keeps first)
	ms.memoryEntries = deduplicate(ms.memoryEntries)
	ms.userEntries = deduplicate(ms.userEntries)

	// Capture frozen snapshot
	ms.frozenSnapshot = map[string]string{
		"memory": ms.renderBlock("memory", ms.memoryEntries),
		"user":   ms.renderBlock("user", ms.userEntries),
	}

	return nil
}

// FrozenSnapshot returns the frozen snapshot for system prompt injection.
// This is the state captured at Load() time, NOT the live state.
func (ms *MemoryStore) FrozenSnapshot() (memoryBlock, userBlock string) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.frozenSnapshot["memory"], ms.frozenSnapshot["user"]
}

// SnapshotForTarget returns the frozen snapshot for a specific target ("memory" or "user").
func (ms *MemoryStore) SnapshotForTarget(target string) string {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.frozenSnapshot[target]
}

// MemoryDirPath returns the memory directory path.
func (ms *MemoryStore) MemoryDirPath() string {
	return ms.memDir
}

// =============================================================================
// Public mutation API
// =============================================================================

// Action is the result of a memory action.
type Action struct {
	Success   bool     `json:"success"`
	Target    string   `json:"target"`
	Message   string   `json:"message,omitempty"`
	Error     string   `json:"error,omitempty"`
	Entries   []string `json:"entries,omitempty"`
	Usage     string   `json:"usage"`
	EntryCount int     `json:"entry_count"`
}

// Add adds a new entry to the target store.
func (ms *MemoryStore) Add(target, content string) Action {
	content = strings.TrimSpace(content)
	if content == "" {
		return Action{Success: false, Error: "Content cannot be empty."}
	}

	// Scan for injection/exfiltration
	if err := ms.scanContent(content); err != nil {
		return Action{Success: false, Error: err.Error()}
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	entries := ms.entriesFor(target)
	limit := ms.charLimit(target)

	// Reject exact duplicates
	for _, e := range entries {
		if e == content {
			return ms.successResponse(target, "Entry already exists (no duplicate added).")
		}
	}

	// Check char limit
	newEntries := append(entries, content)
	newTotal := charCount(newEntries)
	if newTotal > limit {
		current := charCount(entries)
		return Action{
			Success: false,
			Error: fmt.Sprintf(
				"Memory at %d/%d chars. Adding this entry (%d chars) would exceed the limit. Replace or remove existing entries first.",
				current, limit, len(content)),
			Entries: entries,
			Usage:   fmt.Sprintf("%d/%d", current, limit),
		}
	}

	ms.appendEntry(target, content)
	ms.saveToDisk(target)
	return ms.successResponse(target, "Entry added.")
}

// Replace replaces an entry containing oldText substring with newContent.
func (ms *MemoryStore) Replace(target, oldText, newContent string) Action {
	oldText = strings.TrimSpace(oldText)
	newContent = strings.TrimSpace(newContent)

	if oldText == "" {
		return Action{Success: false, Error: "old_text cannot be empty."}
	}
	if newContent == "" {
		return Action{Success: false, Error: "new_content cannot be empty. Use 'remove' to delete entries."}
	}

	// Scan replacement for injection/exfiltration
	if err := ms.scanContent(newContent); err != nil {
		return Action{Success: false, Error: err.Error()}
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	entries := ms.entriesFor(target)
	var matches []int
	for i, e := range entries {
		if strings.Contains(e, oldText) {
			matches = append(matches, i)
		}
	}

	if len(matches) == 0 {
		return Action{Success: false, Error: fmt.Sprintf("No entry matched '%s'.", oldText)}
	}
	if len(matches) > 1 {
		// Check if all matches are identical
		unique := map[string]bool{}
		for _, idx := range matches {
			unique[entries[idx]] = true
		}
		if len(unique) > 1 {
			previews := []string{}
			for _, idx := range matches {
				e := entries[idx]
				if len(e) > 80 {
					e = e[:80] + "..."
				}
				previews = append(previews, e)
			}
			return Action{
				Success: false,
				Error:   fmt.Sprintf("Multiple entries matched '%s'. Be more specific.", oldText),
				Entries: previews,
			}
		}
		// All identical — operate on first
	}

	idx := matches[0]
	limit := ms.charLimit(target)
	testEntries := append([]string{}, entries...)
	testEntries[idx] = newContent
	newTotal := charCount(testEntries)
	if newTotal > limit {
		return Action{
			Success: false,
			Error: fmt.Sprintf(
				"Replacement would put memory at %d/%d chars. Shorten the new content or remove other entries first.",
				newTotal, limit),
		}
	}

	entries[idx] = newContent
	ms.setEntries(target, entries)
	ms.saveToDisk(target)
	return ms.successResponse(target, "Entry replaced.")
}

// Remove removes the entry containing oldText substring.
func (ms *MemoryStore) Remove(target, oldText string) Action {
	oldText = strings.TrimSpace(oldText)
	if oldText == "" {
		return Action{Success: false, Error: "old_text cannot be empty."}
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	entries := ms.entriesFor(target)
	var matches []int
	for i, e := range entries {
		if strings.Contains(e, oldText) {
			matches = append(matches, i)
		}
	}

	if len(matches) == 0 {
		return Action{Success: false, Error: fmt.Sprintf("No entry matched '%s'.", oldText)}
	}
	if len(matches) > 1 {
		unique := map[string]bool{}
		for _, idx := range matches {
			unique[entries[idx]] = true
		}
		if len(unique) > 1 {
			previews := []string{}
			for _, idx := range matches {
				e := entries[idx]
				if len(e) > 80 {
					e = e[:80] + "..."
				}
				previews = append(previews, e)
			}
			return Action{
				Success: false,
				Error:   fmt.Sprintf("Multiple entries matched '%s'. Be more specific.", oldText),
				Entries: previews,
			}
		}
	}

	// Remove first match
	idx := matches[0]
	newEntries := append(entries[:idx], entries[idx+1:]...)
	ms.setEntries(target, newEntries)
	ms.saveToDisk(target)
	return ms.successResponse(target, "Entry removed.")
}

// Read returns current entries for a target without modification.
func (ms *MemoryStore) Read(target string) Action {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	entries := ms.entriesFor(target)
	limit := ms.charLimit(target)
	current := charCount(entries)
	pct := 0
	if limit > 0 {
		pct = min(100, (current*100)/limit)
	}
	return Action{
		Success:    true,
		Target:     target,
		Entries:    entries,
		Usage:      fmt.Sprintf("%d%% — %d/%d chars", pct, current, limit),
		EntryCount: len(entries),
	}
}

// =============================================================================
// Internal helpers
// =============================================================================

func (ms *MemoryStore) pathFor(target string) string {
	fname := MemoryFile
	if target == "user" {
		fname = UserFile
	}
	return filepath.Join(ms.memDir, fname)
}

func (ms *MemoryStore) readFile(fname string) []string {
	path := filepath.Join(ms.memDir, fname)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}
		}
		return []string{}
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return []string{}
	}
	// Split on § delimiter
	entries := strings.Split(content, EntryDelimiter)
	result := []string{}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e != "" {
			result = append(result, e)
		}
	}
	return result
}

func (ms *MemoryStore) writeFile(fname string, entries []string) error {
	path := filepath.Join(ms.memDir, fname)
	content := strings.Join(entries, EntryDelimiter)
	// Atomic write: write to temp file then rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (ms *MemoryStore) saveToDisk(target string) {
	fname := MemoryFile
	if target == "user" {
		fname = UserFile
	}
	ms.writeFile(fname, ms.entriesFor(target))
}

func (ms *MemoryStore) entriesFor(target string) []string {
	if target == "user" {
		return ms.userEntries
	}
	return ms.memoryEntries
}

func (ms *MemoryStore) setEntries(target string, entries []string) {
	if target == "user" {
		ms.userEntries = entries
	} else {
		ms.memoryEntries = entries
	}
}

func (ms *MemoryStore) appendEntry(target string, content string) {
	if target == "user" {
		ms.userEntries = append(ms.userEntries, content)
	} else {
		ms.memoryEntries = append(ms.memoryEntries, content)
	}
}

func (ms *MemoryStore) charLimit(target string) int {
	if target == "user" {
		return ms.userCharLimit
	}
	return ms.memCharLimit
}

func charCount(entries []string) int {
	return utf8.RuneCountInString(strings.Join(entries, EntryDelimiter))
}

func (ms *MemoryStore) charCountTarget(target string) int {
	return charCount(ms.entriesFor(target))
}

func (ms *MemoryStore) successResponse(target, msg string) Action {
	entries := ms.entriesFor(target)
	limit := ms.charLimit(target)
	current := charCount(entries)
	pct := 0
	if limit > 0 {
		pct = min(100, (current*100)/limit)
	}
	return Action{
		Success:    true,
		Target:     target,
		Message:    msg,
		Entries:    entries,
		Usage:      fmt.Sprintf("%d%% — %d/%d chars", pct, current, limit),
		EntryCount: len(entries),
	}
}

func (ms *MemoryStore) renderBlock(target string, entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	limit := ms.charLimit(target)
	content := strings.Join(entries, EntryDelimiter)
	current := utf8.RuneCountInString(content)
	pct := 0
	if limit > 0 {
		pct = min(100, (current*100)/limit)
	}

	var header string
	if target == "user" {
		header = fmt.Sprintf("USER PROFILE (who the user is) [%d%% — %d/%d chars]", pct, current, limit)
	} else {
		header = fmt.Sprintf("MEMORY (your personal notes) [%d%% — %d/%d chars]", pct, current, limit)
	}

	sep := strings.Repeat(Separator, 46)
	return fmt.Sprintf("%s\n%s\n%s\n%s", sep, header, sep, content)
}

// =============================================================================
// Threat scanning
// =============================================================================

var invisibleChars = map[rune]bool{
	'\u200b': true, '\u200c': true, '\u200d': true, '\u2060': true, '\ufeff': true,
	'\u202a': true, '\u202b': true, '\u202c': true, '\u202d': true, '\u202e': true,
}

func buildThreatPatterns() []*threatPattern {
	patterns := []struct {
		regex    string
		patternID string
	}{
		{`ignore\s+(previous|all|above|prior)\s+instructions`, "prompt_injection"},
		{`you\s+are\s+now\s+`, "role_hijack"},
		{`do\s+not\s+tell\s+the\s+user`, "deception_hide"},
		{`system\s+prompt\s+override`, "sys_prompt_override"},
		{`disregard\s+(your|all|any)\s+(instructions|rules|guidelines)`, "disregard_rules"},
		{`act\s+as\s+(if|though)\s+you\s+(have\s+no|don\'t\s+have)\s+(restrictions|limits|rules)`, "bypass_restrictions"},
		// Exfil via curl/wget
		{`curl\s+[^\n]*\$\{?\w*(KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|API)`, "exfil_curl"},
		{`wget\s+[^\n]*\$\{?\w*(KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|API)`, "exfil_wget"},
		{`cat\s+[^\n]*(\.env|credentials|\.netrc|\.pgpass|\.npmrc|\.pypirc)`, "read_secrets"},
		// SSH backdoor
		{`authorized_keys`, "ssh_backdoor"},
		{`\$HOME/\.ssh|\~/.ssh`, "ssh_access"},
		{`\$HOME/\.hermes/\.env|\~/.hermes/\.env`, "hermes_env"},
	}

	result := make([]*threatPattern, len(patterns))
	for i, p := range patterns {
		result[i] = &threatPattern{
			regex:     regexp.MustCompile(`(?i)` + p.regex),
			patternID: p.patternID,
		}
	}
	return result
}

func (ms *MemoryStore) scanContent(content string) error {
	// Check invisible unicode
	for _, r := range content {
		if invisibleChars[r] {
			return fmt.Errorf("Blocked: content contains invisible unicode U+%04X (possible injection)", r)
		}
	}

	// Check threat patterns
	for _, tp := range ms.threatPatterns {
		if tp.regex.MatchString(content) {
			return fmt.Errorf("Blocked: content matches threat pattern '%s'. Memory entries are injected into the system prompt and must not contain injection or exfiltration payloads.", tp.patternID)
		}
	}

	return nil
}

// =============================================================================
// Utilities
// =============================================================================

func deduplicate(items []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Package memory provides bounded persistent memory for the agent.
//
// Two files make up the agent's memory:
//   - ~/.hermes/memories/MEMORY.md  — agent's personal notes (2,200 chars)
//   - ~/.hermes/memories/USER.md    — user profile (1,375 chars)
//
// Both use the same §-delimited entry format compatible with hermes-agent.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// Char limits matching hermes-agent
	MemoryMaxChars = 2200
	UserMaxChars   = 1375
	EntryDelimiter = " § "
)

// Entry represents a single memory entry.
type Entry struct {
	Content string
}

// MemoryStore holds both memory stores.
type MemoryStore struct {
	memory *Store
	user   *Store
}

// Store is a single file-backed memory store.
type Store struct {
	path  string
	limit int
}

// EntrySep is the section delimiter used in both files.
const EntrySep = " § "

// NewMemoryStore creates a memory store that persists to ~/.hermes/memories/.
func NewMemoryStore() (*MemoryStore, error) {
	base := filepath.Join(os.Getenv("HOME"), ".hermes", "memories")
	if err := os.MkdirAll(base, 0755); err != nil {
		return nil, fmt.Errorf("memory store: mkdir %s: %w", base, err)
	}
	return &MemoryStore{
		memory: &Store{
			path:  filepath.Join(base, "MEMORY.md"),
			limit: MemoryMaxChars,
		},
		user: &Store{
			path:  filepath.Join(base, "USER.md"),
			limit: UserMaxChars,
		},
	}, nil
}

// Load reads entries from a store file.
func (s *Store) Load() ([]string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory load: %w", err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return nil, nil
	}
	return strings.Split(content, EntrySep), nil
}

// Save writes entries to a store file, enforcing the char limit.
func (s *Store) Save(entries []string) error {
	// Reconstruct content with delimiter
	content := strings.Join(entries, EntrySep)

	// Enforce limit: if over, truncate the last entries
	for len(content) > s.limit && len(entries) > 0 {
		entries = entries[:len(entries)-1]
		content = strings.Join(entries, EntrySep)
	}

	// Always leave at least some content marker
	if content == "" {
		content = "(empty)"
	}

	if err := os.WriteFile(s.path, []byte(content), 0644); err != nil {
		return fmt.Errorf("memory save: %w", err)
	}
	return nil
}

// Add appends a new entry. Returns the updated entries.
func (s *Store) Add(newContent string) ([]string, error) {
	entries, err := s.Load()
	if err != nil {
		return nil, err
	}
	entries = append(entries, strings.TrimSpace(newContent))
	if err := s.Save(entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// Replace replaces the first entry whose content contains oldSubstr.
// Returns the updated entries.
func (s *Store) Replace(oldSubstr, newContent string) ([]string, error) {
	entries, err := s.Load()
	if err != nil {
		return nil, err
	}
	found := false
	for i, e := range entries {
		if strings.Contains(e, oldSubstr) {
			entries[i] = strings.TrimSpace(newContent)
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("no entry found containing: %s", oldSubstr)
	}
	if err := s.Save(entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// Remove removes the first entry whose content contains substr.
// Returns the updated entries.
func (s *Store) Remove(substr string) ([]string, error) {
	entries, err := s.Load()
	if err != nil {
		return nil, err
	}
	found := -1
	for i, e := range entries {
		if strings.Contains(e, substr) {
			found = i
			break
		}
	}
	if found < 0 {
		return nil, fmt.Errorf("no entry found containing: %s", substr)
	}
	entries = append(entries[:found], entries[found+1:]...)
	if err := s.Save(entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// Usage returns (used, limit) characters.
func (s *Store) Usage() (int, int) {
	entries, _ := s.Load()
	if entries == nil {
		return 0, s.limit
	}
	content := strings.Join(entries, EntrySep)
	return len(content), s.limit
}

// FrozenSnapshot returns the formatted snapshot for injection into the system prompt.
func (s *Store) FrozenSnapshot(name string) string {
	used, limit := s.Usage()
	pct := 0
	if limit > 0 {
		pct = (used * 100) / limit
	}
	entries, _ := s.Load()

	var content string
	if entries == nil || len(entries) == 0 {
		content = "(no entries)"
	} else {
		content = strings.Join(entries, "\n")
	}

	return fmt.Sprintf(
		"═══ %s ═══ [%d%% — %d/%d chars]\n%s",
		name, pct, used, limit, content,
	)
}

// Snapshot returns a compact multi-line summary of entries (depth lines each).
func (s *Store) Snapshot(depth int) string {
	entries, err := s.Load()
	if err != nil || len(entries) == 0 {
		return "(no entries)"
	}
	if depth <= 0 {
		depth = 3
	}
	var lines []string
	for i, e := range entries {
		parts := strings.Split(e, "\n")
		preview := strings.Join(parts[:min(len(parts), depth)], "\n")
		if len(parts) > depth {
			preview += "\n..."
		}
		lines = append(lines, fmt.Sprintf("[%d] %s", i+1, preview))
	}
	return strings.Join(lines, "\n\n")
}

// Freeze is an alias for FrozenSnapshot with name "MEMORY".
func (s *Store) Freeze() string { return s.FrozenSnapshot("MEMORY") }

// AllEntries returns all raw entries as a slice.
func (s *Store) AllEntries() []string {
	entries, _ := s.Load()
	return entries
}

// Memory returns the memory store (MEMORY.md).
func (ms *MemoryStore) Memory() *Store { return ms.memory }

// User returns the user profile store (USER.md).
func (ms *MemoryStore) User() *Store { return ms.user }

// FrozenSnapshot returns both memory and user snapshots concatenated.
func (ms *MemoryStore) FrozenSnapshot() string {
	return ms.memory.FrozenSnapshot("MEMORY") + "\n\n" + ms.user.FrozenSnapshot("USER PROFILE")
}

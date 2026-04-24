package tools

import (
	"fmt"
	"strings"
	"sync"
)

// Valid todo statuses.
const (
	TodoStatusPending    = "pending"
	TodoStatusInProgress  = "in_progress"
	TodoStatusCompleted  = "completed"
	TodoStatusCancelled  = "cancelled"
)

// TodoStore is an in-memory, thread-safe task list.
// One instance lives on the AIAgent (one per session).
type TodoStore struct {
	mu     sync.RWMutex
	items  []TodoItem
}

// TodoItem represents a single task.
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// Write updates the store. If merge is false, replaces the entire list.
// If merge is true, updates existing items by id and appends new ones.
func (s *TodoStore) Write(todos []TodoItem, merge bool) []TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !merge {
		s.items = s.dedupeByID(validateAll(todos))
	} else {
		existing := make(map[string]*TodoItem)
		for i := range s.items {
			existing[s.items[i].ID] = &s.items[i]
		}
		for _, t := range s.dedupeByID(todos) {
			if t.ID == "" {
				continue
			}
			if existing[t.ID] != nil {
				if t.Content != "" {
					existing[t.ID].Content = t.Content
				}
				if isValidStatus(t.Status) {
					existing[t.ID].Status = t.Status
				}
			} else {
				s.items = append(s.items, t)
				existing[t.ID] = &s.items[len(s.items)-1]
			}
		}
		// Rebuild preserving order for existing items
		seen := make(map[string]bool)
		var rebuilt []TodoItem
		for _, item := range s.items {
			if !seen[item.ID] {
				rebuilt = append(rebuilt, item)
				seen[item.ID] = true
			}
		}
		s.items = rebuilt
	}
	return s.readUnsafe()
}

func (s *TodoStore) readUnsafe() []TodoItem {
	result := make([]TodoItem, len(s.items))
	for i := range s.items {
		result[i] = s.items[i]
	}
	return result
}

// Read returns a copy of the current list.
func (s *TodoStore) Read() []TodoItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readUnsafe()
}

// HasItems returns true if the list is non-empty.
func (s *TodoStore) HasItems() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items) > 0
}

// FormatForInjection returns a human-readable string for post-compression injection,
// or empty string if no active (pending/in_progress) items exist.
func (s *TodoStore) FormatForInjection() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var active []TodoItem
	for _, item := range s.items {
		if item.Status == TodoStatusPending || item.Status == TodoStatusInProgress {
			active = append(active, item)
		}
	}
	if len(active) == 0 {
		return ""
	}

	markers := map[string]string{
		TodoStatusCompleted: "[x]",
		TodoStatusInProgress: "[>]",
		TodoStatusPending:    "[ ]",
		TodoStatusCancelled:  "[~]",
	}

	var b strings.Builder
	b.WriteString("[Your active task list was preserved across context compression]\n")
	for _, item := range active {
		marker := markers[item.Status]
		if marker == "" {
			marker = "[?]"
		}
		fmt.Fprintf(&b, "- %s %s. %s (%s)\n", marker, item.ID, item.Content, item.Status)
	}
	return b.String()
}

func isValidStatus(status string) bool {
	switch status {
	case TodoStatusPending, TodoStatusInProgress, TodoStatusCompleted, TodoStatusCancelled:
		return true
	}
	return false
}

func validateAll(todos []TodoItem) []TodoItem {
	result := make([]TodoItem, 0, len(todos))
	seen := make(map[string]bool)
	for _, t := range todos {
		id := strings.TrimSpace(t.ID)
		if id == "" {
			id = "?"
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		content := strings.TrimSpace(t.Content)
		if content == "" {
			content = "(no description)"
		}
		status := strings.TrimSpace(strings.ToLower(t.Status))
		if !isValidStatus(status) {
			status = TodoStatusPending
		}
		result = append(result, TodoItem{ID: id, Content: content, Status: status})
	}
	return result
}

func (s *TodoStore) dedupeByID(todos []TodoItem) []TodoItem {
	lastIndex := make(map[string]int)
	for i, t := range todos {
		id := t.ID
		if id == "" {
			id = "?"
		}
		lastIndex[id] = i
	}
	unique := make([]TodoItem, 0, len(lastIndex))
	for _, idx := range lastIndex {
		unique = append(unique, todos[idx])
	}
	return unique
}

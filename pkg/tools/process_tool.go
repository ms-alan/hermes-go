package tools

import (
	"sync"
	"time"
)

// ProcessInfo describes a background process.
type ProcessInfo struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	StartedAt time.Time `json:"startedAt"`
	SessionID string    `json:"sessionId,omitempty"`
}

// ProcessRegistry tracks background processes.
type ProcessRegistry struct {
	mu       sync.RWMutex
	processes map[string]*ProcessInfo
}

var globalProcessRegistry = &ProcessRegistry{processes: make(map[string]*ProcessInfo)}

// Register adds a process to the registry.
func (r *ProcessRegistry) Register(id, command, sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.processes[id] = &ProcessInfo{
		ID:        id,
		Command:   command,
		StartedAt: time.Now(),
		SessionID: sessionID,
	}
}

// Unregister removes a process from the registry.
func (r *ProcessRegistry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.processes, id)
}

// Get returns a process by ID.
func (r *ProcessRegistry) Get(id string) *ProcessInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.processes[id]
}

// List returns all processes.
func (r *ProcessRegistry) List() []*ProcessInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*ProcessInfo, 0, len(r.processes))
	for _, p := range r.processes {
		out = append(out, p)
	}
	return out
}

// processToolHandler implements the "process" tool for background job management.
func processToolHandler(args map[string]any) string {
	action, _ := args["action"].(string)
	sessionID, _ := args["sessionId"].(string)

	switch action {
	case "list":
		procs := globalProcessRegistry.List()
		type processList struct {
			Count   int            `json:"count"`
			Details []*ProcessInfo `json:"details"`
		}
		return toolResultData(map[string]any{
			"count":    len(procs),
			"processes": procs,
		})

	case "get":
		id, _ := args["id"].(string)
		if id == "" {
			return toolError("id is required for get")
		}
		proc := globalProcessRegistry.Get(id)
		if proc == nil {
			return toolError("process not found: " + id)
		}
		return toolResult("process", proc)

	case "register":
		id, _ := args["id"].(string)
		command, _ := args["command"].(string)
		if id == "" || command == "" {
			return toolError("id and command are required for register")
		}
		globalProcessRegistry.Register(id, command, sessionID)
		return toolResult("registered", id)

	case "unregister":
		id, _ := args["id"].(string)
		if id == "" {
			return toolError("id is required for unregister")
		}
		globalProcessRegistry.Unregister(id)
		return toolResult("unregistered", id)

	default:
		return toolError("unknown action: "+action, "expected: list/get/register/unregister")
	}
}

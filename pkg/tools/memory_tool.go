package tools

import (
	"fmt"
	"strings"

	"github.com/nousresearch/hermes-go/pkg/memory"
)

// memoryStore is the global memory store instance (set at startup via SetMemoryStore).
var memoryStore *memory.MemoryStore

// SetMemoryStore configures the global memory store used by the memory tool.
func SetMemoryStore(ms *memory.MemoryStore) {
	memoryStore = ms
}

func getTargetStore(target string) *memory.Store {
	if memoryStore == nil {
		return nil
	}
	if target == "user" {
		return memoryStore.User()
	}
	return memoryStore.Memory() // default: memory
}

// memoryToolHandler is registered as the "memory" tool.
func memoryToolHandler(args map[string]any) string {
	action, _ := args["action"].(string)
	target, _ := args["target"].(string)
	if target == "" {
		target = "memory"
	}

	store := getTargetStore(target)
	if store == nil {
		return toolError("memory store not configured")
	}

	switch action {
	case "add":
		return handleAdd(store, args)
	case "replace":
		return handleReplace(store, args)
	case "remove":
		return handleRemove(store, args)
	case "snapshot":
		return handleSnapshot(store, args)
	case "freeze":
		return handleFreeze(store)
	case "show":
		return handleShow(store)
	default:
		return toolError("unknown action: "+action, "expected: add/replace/remove/snapshot/freeze/show")
	}
}

func handleAdd(store *memory.Store, args map[string]any) string {
	content, _ := args["content"].(string)
	if content == "" {
		return toolError("content is required for add")
	}
	entries, err := store.Add(content)
	if err != nil {
		return toolError("add failed: %v", err)
	}
	used, limit := store.Usage()
	return toolResult("msg", fmt.Sprintf("Added. Now %d/%d chars. Entries:\n%s", used, limit, strings.Join(entries, "\n")))
}

func handleReplace(store *memory.Store, args map[string]any) string {
	oldText, _ := args["old_text"].(string)
	newContent, _ := args["content"].(string)
	if oldText == "" || newContent == "" {
		return toolError("old_text and content are required for replace")
	}
	entries, err := store.Replace(oldText, newContent)
	if err != nil {
		return toolError("replace failed: %v", err)
	}
	used, limit := store.Usage()
	return toolResult("msg", fmt.Sprintf("Replaced. Now %d/%d chars. Entries:\n%s", used, limit, strings.Join(entries, "\n")))
}

func handleRemove(store *memory.Store, args map[string]any) string {
	oldText, _ := args["old_text"].(string)
	if oldText == "" {
		return toolError("old_text is required for remove")
	}
	entries, err := store.Remove(oldText)
	if err != nil {
		return toolError("remove failed: %v", err)
	}
	used, limit := store.Usage()
	return toolResult("msg", fmt.Sprintf("Removed. Now %d/%d chars. Entries:\n%s", used, limit, strings.Join(entries, "\n")))
}

func handleSnapshot(store *memory.Store, args map[string]any) string {
	depth := 3
	if d, ok := args["depth"].(float64); ok {
		depth = int(d)
	}
	snapshot := store.Snapshot(depth)
	return toolResult("snapshot", snapshot)
}

func handleFreeze(store *memory.Store) string {
	return toolResult("frozen", store.Freeze())
}

func handleShow(store *memory.Store) string {
	return toolResult("entries", store.AllEntries())
}

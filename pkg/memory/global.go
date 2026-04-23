package memory

import (
	"sync"
)

var (
	globalMemoryStore     *MemoryStore
	globalMemoryStoreOnce sync.Once
)

// SetMemoryStore configures the global memory store.
func SetMemoryStore(ms *MemoryStore) {
	globalMemoryStore = ms
}

// GetMemoryStore returns the global memory store (nil if not configured).
func GetMemoryStore() *MemoryStore {
	return globalMemoryStore
}

// Global returns a ready-to-use global MemoryStore.
// If no store has been configured, a new one is created lazily.
func Global() *MemoryStore {
	if globalMemoryStore != nil {
		return globalMemoryStore
	}
	globalMemoryStoreOnce.Do(func() {
		globalMemoryStore = NewMemoryStore()
	})
	return globalMemoryStore
}

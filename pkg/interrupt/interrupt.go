package interrupt

import (
	"sync"
	"unsafe"
)

// Per-goroutine interrupt registry.
// Each goroutine has its own interrupt flag so that interrupting one agent
// does not affect tools running in other concurrent agents (critical for
// gateway where multiple sessions run in the same process).

var (
	_interrupted sync.Map // map[uint64]struct{} — key = goroutine ID
	_gid         sync.Map // map[uintptr]uint64 — stackAddr → gid (goroutine-local cache)
	_lock        sync.Mutex
	_counter     uint64
)

// getGID returns a stable pseudo-unique ID for the current goroutine.
// IDs are per-goroutine: the same goroutine always gets the same ID.
// We cache the ID per goroutine using its stack base address as the key.
func getGID() uint64 {
	// Use the address of a stack slot as goroutine identity.
	// Every goroutine has its own stack, so this address is unique per goroutine.
	// We cache the mapping stackAddr → gid in _gid so the same goroutine
	// always gets the same gid on repeated calls.
	var slot int
	stackPtr := uintptr(unsafe.Pointer(&slot))

	// Check cache first
	if cached, ok := _gid.Load(stackPtr); ok {
		return cached.(uint64)
	}

	// Not cached: allocate a new ID
	_lock.Lock()
	gid := _counter
	_counter++
	_lock.Unlock()

	_gid.Store(stackPtr, gid)
	return gid
}

// SetInterrupt sets (active=true) or clears (active=false) the interrupt flag
// for the current goroutine. Thread-safe.
func SetInterrupt(active bool) {
	gid := getGID()
	if active {
		_interrupted.Store(gid, struct{}{})
	} else {
		_interrupted.Delete(gid)
	}
}

// IsInterrupted returns true if the current goroutine has been interrupted.
// Safe to call from any goroutine — each goroutine only sees its own state.
func IsInterrupted() bool {
	gid := getGID()
	_, ok := _interrupted.Load(gid)
	return ok
}

// ClearInterrupt clears the interrupt flag for the current goroutine.
func ClearInterrupt() {
	_interrupted.Delete(getGID())
}

package context

import (
	"crypto/sha256"
	"encoding/hex"
	"hash/fnv"
	"strings"
	"sync"
	"time"
)

// CacheEntry holds a cached value with its expiration time.
type CacheEntry struct {
	Value     any
	ExpiresAt time.Time
}

// TTLCache is an in-memory cache with TTL-based expiration.
// Keys are derived from content using FNV-1a hash for fast lookups,
// but the SHA256 hash is stored alongside for cache-busting / diagnostics.
type TTLCache struct {
	mu       sync.RWMutex
	items    map[uint64]*CacheEntry
	sha256   map[uint64]string // uint64 hash -> hex SHA256 for collision detection
	defaultTTL time.Duration
	clock    func() time.Time // allows testing with a virtual clock
}

// NewTTLCache creates a new cache with the given default TTL.
func NewTTLCache(ttl time.Duration) *TTLCache {
	return &TTLCache{
		items:      make(map[uint64]*CacheEntry),
		sha256:     make(map[uint64]string),
		defaultTTL: ttl,
		clock:      time.Now,
	}
}

// Key holds the components used to build a cache key.
type Key struct {
	SystemPrompt string
	Model        string
	Tools        []string
	Extra        string
}

// BuildKey computes a fast uint64 key (FNV-1a) and returns the SHA256 hex for
// diagnostics.  The FNV key is used for map lookups; SHA256 is stored to
// detect (unlikely) hash collisions.
func (k Key) BuildKey() (uint64, string) {
	// Build a deterministic string for hashing.
	var sb strings.Builder
	sb.WriteString(k.SystemPrompt)
	sb.WriteByte(0)
	sb.WriteString(k.Model)
	sb.WriteByte(0)
	for _, t := range k.Tools {
		sb.WriteString(t)
		sb.WriteByte(0)
	}
	sb.WriteString(k.Extra)
	data := sb.String()

	// FNV-1a 64-bit for the map key.
	h := fnv.New64a()
	h.Write([]byte(data))
	fnvKey := h.Sum64()

	// SHA256 for collision detection / external keys.
	sha := sha256.Sum256([]byte(data))
	shaHex := hex.EncodeToString(sha[:])

	return fnvKey, shaHex
}

// Get retrieves a cached value if it exists and is not expired.
func (c *TTLCache) Get(fnvKey uint64, shaHex string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.items[fnvKey]
	if !ok {
		return nil, false
	}
	// Verify SHA256 to rule out collision.
	if sha, ok := c.sha256[fnvKey]; ok && sha != shaHex {
		// Collision detected — treat as miss.
		return nil, false
	}
	if time.Now().After(entry.ExpiresAt) {
		return nil, false
	}
	return entry.Value, true
}

// Set stores a value in the cache with the default TTL.
func (c *TTLCache) Set(fnvKey uint64, shaHex string, value any) {
	c.SetWithTTL(fnvKey, shaHex, value, c.defaultTTL)
}

// SetWithTTL stores a value with an explicit TTL.
func (c *TTLCache) SetWithTTL(fnvKey uint64, shaHex string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[fnvKey] = &CacheEntry{
		Value:     value,
		ExpiresAt: c.clock().Add(ttl),
	}
	c.sha256[fnvKey] = shaHex
}

// Delete removes an entry from the cache.
func (c *TTLCache) Delete(fnvKey uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.items, fnvKey)
	delete(c.sha256, fnvKey)
}

// Purge removes all entries.
func (c *TTLCache) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[uint64]*CacheEntry)
	c.sha256 = make(map[uint64]string)
}

// Cleanup removes all expired entries.  It is safe to call concurrently.
func (c *TTLCache) Cleanup() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock()
	removed := 0
	for k, v := range c.items {
		if now.After(v.ExpiresAt) {
			delete(c.items, k)
			delete(c.sha256, k)
			removed++
		}
	}
	return removed
}

// Len returns the number of items in the cache (including expired ones not yet cleaned up).
func (c *TTLCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// Stats holds cache statistics for debugging / observability.
type Stats struct {
	Items       int
	AvgTTLSecs  float64
	OldestEntry time.Duration
}

// Stats returns approximate cache statistics.
func (c *TTLCache) Stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.items) == 0 {
		return Stats{}
	}
	now := c.clock()
	var totalTTL, oldest time.Duration
	for _, v := range c.items {
		ttl := v.ExpiresAt.Sub(now)
		totalTTL += ttl
		if ttl > oldest || oldest == 0 {
			oldest = ttl
		}
	}
	return Stats{
		Items:       len(c.items),
		AvgTTLSecs:  totalTTL.Seconds() / float64(len(c.items)),
		OldestEntry: oldest,
	}
}

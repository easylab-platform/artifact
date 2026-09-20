package artifactkit

import (
	"sync"
	"time"
)

// MemCache is a process-wide TTL cache for upstream index/metadata documents.
// Caching the index layer (not just artifacts) is what makes a mirror fast
// for repeated builds; otherwise every client resolution re-fetches the same
// small index documents through the proxy.
type MemCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	cap  int
	data map[string]*entry
}

// IndexCache is the type returned by SharedIndexCache for adapter use.
type IndexCache = MemCache

type entry struct {
	body    string
	expires time.Time
}

// DefaultIndexTTL is how long upstream index responses are served before a
// re-fetch.
const DefaultIndexTTL = 60 * 60 * time.Second

// NewMemCache returns a TTL cache with a bound on entries.
func NewMemCache(ttl time.Duration) *MemCache {
	if ttl <= 0 {
		ttl = DefaultIndexTTL
	}
	return &MemCache{ttl: ttl, cap: 4096, data: map[string]*entry{}}
}

// Get returns a cached body, or false when absent/expired.
func (c *MemCache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[key]
	if !ok {
		return "", false
	}
	if time.Now().After(e.expires) {
		delete(c.data, key)
		return "", false
	}
	return e.body, true
}

// Set stores a body with a fresh expiry. At capacity it evicts one expired
// entry when present, else an arbitrary entry, so the bound is always honored
// (a cache that grows without bound under an all-fresh workload is a leak).
func (c *MemCache) Set(key, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.data[key]; !exists && len(c.data) >= c.cap {
		now := time.Now()
		victim := ""
		for k, e := range c.data {
			if victim == "" {
				victim = k // fallback: evict an arbitrary entry
			}
			if now.After(e.expires) {
				victim = k // prefer an expired one
				break
			}
		}
		if victim != "" {
			delete(c.data, victim)
		}
	}
	c.data[key] = &entry{body: body, expires: time.Now().Add(c.ttl)}
}

// SharedIndexCache returns a process-global index cache (1h TTL) used by the
// protocol adapters that have no per-call cache to thread.
func SharedIndexCache() *MemCache {
	once.Do(func() {
		globalIndexCache = NewMemCache(DefaultIndexTTL)
	})
	return globalIndexCache
}

var (
	once             sync.Once
	globalIndexCache *MemCache
)

package github_preview

import (
	"sync"
	"time"

	"github.com/spf13/afero"
)

// metadata cache entry
type metadataEntry struct {
	artifactID int64
	headSHA    string
	resolvedAt time.Time
}

func (m *metadataEntry) isStale(ttl time.Duration) bool {
	return time.Since(m.resolvedAt) > ttl
}

// artifact cache entry
type artifactEntry struct {
	fs         afero.Fs
	sizeBytes  int64
	lastAccess time.Time
	cleanup    func() // removes temp file on disk
}

// MetadataCache maps keys (PR numbers or branch names) to artifact metadata with TTL expiry
type MetadataCache struct {
	mu      sync.RWMutex
	entries map[string]*metadataEntry
	ttl     time.Duration
}

func newMetadataCache(ttl time.Duration) *MetadataCache {
	return &MetadataCache{
		entries: make(map[string]*metadataEntry),
		ttl:     ttl,
	}
}

// get returns the entry and whether it's fresh. returns nil if no entry exists.
func (c *MetadataCache) get(key string) (entry *metadataEntry, fresh bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	return e, !e.isStale(c.ttl)
}

func (c *MetadataCache) set(key string, artifactID int64, headSHA string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &metadataEntry{
		artifactID: artifactID,
		headSHA:    headSHA,
		resolvedAt: time.Now(),
	}
}

func (c *MetadataCache) evict(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[key]
	if ok {
		delete(c.entries, key)
	}
	return ok
}

// snapshot returns a copy of all entries for status reporting
func (c *MetadataCache) snapshot() map[string]*metadataEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]*metadataEntry, len(c.entries))
	for k, v := range c.entries {
		cp := *v
		out[k] = &cp
	}
	return out
}

// ArtifactCache is an LRU cache of artifact filesystems keyed by artifact ID
type ArtifactCache struct {
	mu      sync.RWMutex
	entries map[int64]*artifactEntry
	maxSize int
}

func newArtifactCache(maxSize int) *ArtifactCache {
	return &ArtifactCache{
		entries: make(map[int64]*artifactEntry),
		maxSize: maxSize,
	}
}

func (c *ArtifactCache) get(artifactID int64) (afero.Fs, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[artifactID]
	if !ok {
		return nil, false
	}
	e.lastAccess = time.Now()
	return e.fs, true
}

func (c *ArtifactCache) set(artifactID int64, fs afero.Fs, sizeBytes int64, cleanup func()) {
	var cleanups []func()
	c.mu.Lock()
	// evict LRU if at capacity
	for len(c.entries) >= c.maxSize {
		if fn := c.evictOldestLocked(); fn != nil {
			cleanups = append(cleanups, fn)
		}
	}
	c.entries[artifactID] = &artifactEntry{
		fs:         fs,
		sizeBytes:  sizeBytes,
		lastAccess: time.Now(),
		cleanup:    cleanup,
	}
	c.mu.Unlock()
	// run cleanups outside lock
	for _, fn := range cleanups {
		fn()
	}
}

// evictOldestLocked removes the LRU entry and returns its cleanup func (may be nil).
// must be called with c.mu held.
func (c *ArtifactCache) evictOldestLocked() func() {
	var oldestID int64
	var oldestTime time.Time
	first := true
	for id, e := range c.entries {
		if first || e.lastAccess.Before(oldestTime) {
			oldestID = id
			oldestTime = e.lastAccess
			first = false
		}
	}
	if !first {
		e := c.entries[oldestID]
		delete(c.entries, oldestID)
		return e.cleanup
	}
	return nil
}

func (c *ArtifactCache) evict(artifactID int64) bool {
	c.mu.Lock()
	e, ok := c.entries[artifactID]
	if ok {
		delete(c.entries, artifactID)
	}
	c.mu.Unlock()
	// run cleanup outside lock
	if ok && e.cleanup != nil {
		e.cleanup()
	}
	return ok
}

// cleanupAll removes all temp files for all cached artifacts
func (c *ArtifactCache) cleanupAll() {
	c.mu.Lock()
	var cleanups []func()
	for id, e := range c.entries {
		if e.cleanup != nil {
			cleanups = append(cleanups, e.cleanup)
		}
		delete(c.entries, id)
	}
	c.mu.Unlock()
	for _, fn := range cleanups {
		fn()
	}
}

// staleEntries returns artifact IDs that haven't been accessed within maxAge
func (c *ArtifactCache) staleEntries(maxAge time.Duration) []int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var stale []int64
	cutoff := time.Now().Add(-maxAge)
	for id, e := range c.entries {
		if e.lastAccess.Before(cutoff) {
			stale = append(stale, id)
		}
	}
	return stale
}

// stats returns cache statistics for status reporting
func (c *ArtifactCache) stats() (count int, totalBytes int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, e := range c.entries {
		count++
		totalBytes += e.sizeBytes
	}
	return count, totalBytes
}

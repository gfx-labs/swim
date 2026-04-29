package github_preview

import (
	"sync"
	"time"

	"github.com/spf13/afero"
)

// metadata cache entry
type metadataEntry struct {
	artifactID int64
	etag       string
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
}

// MetadataCache maps PR numbers to artifact metadata with TTL expiry
type MetadataCache struct {
	mu      sync.RWMutex
	entries map[int]*metadataEntry
	ttl     time.Duration
}

func newMetadataCache(ttl time.Duration) *MetadataCache {
	return &MetadataCache{
		entries: make(map[int]*metadataEntry),
		ttl:     ttl,
	}
}

// get returns the entry and whether it's fresh. returns nil if no entry exists.
func (c *MetadataCache) get(pr int) (entry *metadataEntry, fresh bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[pr]
	if !ok {
		return nil, false
	}
	return e, !e.isStale(c.ttl)
}

func (c *MetadataCache) set(pr int, artifactID int64, etag string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[pr] = &metadataEntry{
		artifactID: artifactID,
		etag:       etag,
		resolvedAt: time.Now(),
	}
}

func (c *MetadataCache) evict(pr int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[pr]
	if ok {
		delete(c.entries, pr)
	}
	return ok
}

// snapshot returns a copy of all entries for status reporting
func (c *MetadataCache) snapshot() map[int]*metadataEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[int]*metadataEntry, len(c.entries))
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

func (c *ArtifactCache) set(artifactID int64, fs afero.Fs, sizeBytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// evict LRU if at capacity
	for len(c.entries) >= c.maxSize {
		c.evictOldestLocked()
	}
	c.entries[artifactID] = &artifactEntry{
		fs:         fs,
		sizeBytes:  sizeBytes,
		lastAccess: time.Now(),
	}
}

func (c *ArtifactCache) evictOldestLocked() {
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
		delete(c.entries, oldestID)
		// note: we can't unregister from FileSystems here because we don't
		// know the PR number. the caller (handler) is responsible for
		// keeping the global map in sync when it detects a cache miss.
	}
}

func (c *ArtifactCache) evict(artifactID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[artifactID]
	if ok {
		delete(c.entries, artifactID)
	}
	return ok
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

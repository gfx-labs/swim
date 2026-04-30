package github_preview

import (
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

func TestMetadataCacheGetSet(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		artifactID int64
		headSHA    string
	}{
		{
			name:       "simple pr",
			key:        "pr:42",
			artifactID: 100,
			headSHA:    "abc123",
		},
		{
			name:       "large pr number",
			key:        "pr:123",
			artifactID: 200,
			headSHA:    "def456",
		},
		{
			name:       "empty headSHA",
			key:        "pr:7",
			artifactID: 300,
			headSHA:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newMetadataCache(time.Hour)

			// miss before set
			entry, fresh := c.get(tt.key)
			require.Nil(t, entry)
			require.False(t, fresh)

			c.set(tt.key, tt.artifactID, tt.headSHA)

			entry, fresh = c.get(tt.key)
			require.NotNil(t, entry)
			require.True(t, fresh)
			require.Equal(t, tt.artifactID, entry.artifactID)
			require.Equal(t, tt.headSHA, entry.headSHA)
		})
	}
}

func TestMetadataCacheTTL(t *testing.T) {
	ttl := 10 * time.Millisecond
	c := newMetadataCache(ttl)

	c.set("pr:42", 1, "sha1")

	// fresh immediately
	entry, fresh := c.get("pr:42")
	require.NotNil(t, entry)
	require.True(t, fresh)

	// wait for expiry
	time.Sleep(20 * time.Millisecond)

	// stale after TTL but entry still returned
	entry, fresh = c.get("pr:42")
	require.NotNil(t, entry, "entry should still be returned even when stale")
	require.False(t, fresh, "entry should be stale after TTL")
	require.Equal(t, int64(1), entry.artifactID)
}

func TestMetadataCacheEvict(t *testing.T) {
	tests := []struct {
		name   string
		keys   []string
		evict  string
		wantOk bool
	}{
		{
			name:   "evict existing entry",
			keys:   []string{"pr:42", "pr:123"},
			evict:  "pr:42",
			wantOk: true,
		},
		{
			name:   "evict nonexistent entry",
			keys:   []string{"pr:42"},
			evict:  "pr:999",
			wantOk: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newMetadataCache(time.Hour)
			for i, key := range tt.keys {
				c.set(key, int64(i), "sha")
			}

			ok := c.evict(tt.evict)
			require.Equal(t, tt.wantOk, ok)

			entry, _ := c.get(tt.evict)
			require.Nil(t, entry, "entry should be gone after eviction")
		})
	}
}

func TestMetadataCacheSnapshot(t *testing.T) {
	c := newMetadataCache(time.Hour)
	c.set("pr:42", 1, "sha1")
	c.set("pr:123", 2, "sha2")

	snap := c.snapshot()
	require.Len(t, snap, 2)
	require.Equal(t, int64(1), snap["pr:42"].artifactID)
	require.Equal(t, int64(2), snap["pr:123"].artifactID)

	// mutating the snapshot should not affect the cache
	snap["pr:42"].headSHA = "mutated"
	original, _ := c.get("pr:42")
	require.Equal(t, "sha1", original.headSHA, "snapshot mutation must not affect cache")

	// adding to the snapshot should not affect the cache
	snap["pr:999"] = &metadataEntry{}
	snap2 := c.snapshot()
	require.Len(t, snap2, 2)
}

func TestArtifactCacheGetSet(t *testing.T) {
	tests := []struct {
		name       string
		artifactID int64
		sizeBytes  int64
	}{
		{
			name:       "small artifact",
			artifactID: 10,
			sizeBytes:  1024,
		},
		{
			name:       "large artifact",
			artifactID: 20,
			sizeBytes:  1024 * 1024 * 50,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newArtifactCache(10)
			memfs := afero.NewMemMapFs()

			// miss before set
			fs, ok := c.get(tt.artifactID)
			require.Nil(t, fs)
			require.False(t, ok)

			c.set(tt.artifactID, memfs, tt.sizeBytes, nil)

			fs, ok = c.get(tt.artifactID)
			require.NotNil(t, fs)
			require.True(t, ok)
		})
	}
}

func TestArtifactCacheLRU(t *testing.T) {
	c := newArtifactCache(2)

	fs1 := afero.NewMemMapFs()
	fs2 := afero.NewMemMapFs()
	fs3 := afero.NewMemMapFs()

	c.set(1, fs1, 100, nil)
	// small gap so lastAccess ordering is deterministic
	time.Sleep(time.Millisecond)
	c.set(2, fs2, 200, nil)

	// both present
	_, ok := c.get(1)
	require.True(t, ok)
	_, ok = c.get(2)
	require.True(t, ok)

	// access 1 so it becomes more recent than 2
	time.Sleep(time.Millisecond)
	_, ok = c.get(1)
	require.True(t, ok)

	// inserting 3 should evict 2 (the least recently accessed)
	c.set(3, fs3, 300, nil)

	_, ok = c.get(1)
	require.True(t, ok, "artifact 1 should survive (recently accessed)")

	_, ok = c.get(2)
	require.False(t, ok, "artifact 2 should be evicted (LRU)")

	_, ok = c.get(3)
	require.True(t, ok, "artifact 3 should be present (just inserted)")
}

func TestArtifactCacheEvict(t *testing.T) {
	tests := []struct {
		name   string
		ids    []int64
		evict  int64
		wantOk bool
	}{
		{
			name:   "evict existing",
			ids:    []int64{1, 2, 3},
			evict:  2,
			wantOk: true,
		},
		{
			name:   "evict nonexistent",
			ids:    []int64{1},
			evict:  99,
			wantOk: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newArtifactCache(10)
			for _, id := range tt.ids {
				c.set(id, afero.NewMemMapFs(), 100, nil)
			}

			ok := c.evict(tt.evict)
			require.Equal(t, tt.wantOk, ok)

			fs, found := c.get(tt.evict)
			require.Nil(t, fs)
			require.False(t, found)
		})
	}
}

func TestArtifactCacheStats(t *testing.T) {
	c := newArtifactCache(10)

	// empty cache
	count, totalBytes := c.stats()
	require.Equal(t, 0, count)
	require.Equal(t, int64(0), totalBytes)

	c.set(1, afero.NewMemMapFs(), 100, nil)
	c.set(2, afero.NewMemMapFs(), 250, nil)
	c.set(3, afero.NewMemMapFs(), 650, nil)

	count, totalBytes = c.stats()
	require.Equal(t, 3, count)
	require.Equal(t, int64(1000), totalBytes)

	// evict one and check again
	c.evict(2)
	count, totalBytes = c.stats()
	require.Equal(t, 2, count)
	require.Equal(t, int64(750), totalBytes)
}

func TestCacheConcurrency(t *testing.T) {
	mc := newMetadataCache(time.Hour)
	ac := newArtifactCache(50)

	var wg sync.WaitGroup
	workers := 20
	iterations := 100

	// concurrent metadata cache access
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			key := "pr:42"
			for j := 0; j < iterations; j++ {
				mc.set(key, int64(id*1000+j), "sha")
				mc.get(key)
				mc.snapshot()
				if j%10 == 0 {
					mc.evict(key)
				}
			}
		}(i)
	}
	wg.Wait()

	// concurrent artifact cache access
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				aid := int64(id*1000 + j)
				ac.set(aid, afero.NewMemMapFs(), 64, nil)
				ac.get(aid)
				ac.stats()
				if j%10 == 0 {
					ac.evict(aid)
				}
			}
		}(i)
	}
	wg.Wait()
}

package github_preview

import (
	"io"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

func newTestFs() afero.Fs {
	fs := afero.NewMemMapFs()
	afero.WriteFile(fs, "small.txt", []byte("hello"), 0o644)          // 5 bytes
	afero.WriteFile(fs, "medium.txt", make([]byte, 100), 0o644)      // 100 bytes
	afero.WriteFile(fs, "large.txt", make([]byte, 500), 0o644)       // 500 bytes
	afero.WriteFile(fs, "huge.txt", make([]byte, 2000), 0o644)       // 2000 bytes
	fs.Mkdir("dir", 0o755)
	afero.WriteFile(fs, "dir/nested.txt", []byte("nested"), 0o644)
	return fs
}

func TestLruCacheFsHit(t *testing.T) {
	base := newTestFs()
	cache := newLruCacheFs(base, 1024)

	// first read -- cache miss, reads from base
	f, err := cache.Open("small.txt")
	require.NoError(t, err)
	data, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, "hello", string(data))
	f.Close()

	// second read -- cache hit
	f, err = cache.Open("small.txt")
	require.NoError(t, err)
	data, err = io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, "hello", string(data))
	f.Close()
}

func TestLruCacheFsEviction(t *testing.T) {
	base := newTestFs()
	// max 550: fits small (5) + large (500) = 505, but not small+medium+large (605)
	// when large is added, medium (LRU tail) is evicted first, then small+large fits
	cache := newLruCacheFs(base, 550).(*lruCacheFs)

	// load small, then medium -- small is LRU tail, medium is head
	f, _ := cache.Open("small.txt")
	f.Close()
	f, _ = cache.Open("medium.txt")
	f.Close()

	require.Equal(t, int64(105), cache.curBytes)
	require.Len(t, cache.entries, 2)

	// load large (500) -- needs 605 total, max is 550
	// evicts small (tail, 5 bytes) -> 600 > 550, still too big
	// evicts medium (new tail, 100 bytes) -> 500 <= 550, fits
	f, _ = cache.Open("large.txt")
	f.Close()

	require.Equal(t, int64(500), cache.curBytes)
	require.Len(t, cache.entries, 1)
	_, ok := cache.entries["large.txt"]
	require.True(t, ok)
}

func TestLruCacheFsLruOrder(t *testing.T) {
	base := newTestFs()
	// fits small (5) + medium (100) but not + large (500)
	cache := newLruCacheFs(base, 200).(*lruCacheFs)

	// load small, then medium
	f, _ := cache.Open("small.txt")
	f.Close()
	f, _ = cache.Open("medium.txt")
	f.Close()

	// touch small to make it most recent
	f, _ = cache.Open("small.txt")
	f.Close()

	// head should be small, tail should be medium
	require.Equal(t, "small.txt", cache.head.name)
	require.Equal(t, "medium.txt", cache.tail.name)
}

func TestLruCacheFsSkipOversized(t *testing.T) {
	base := newTestFs()
	// cache is only 100 bytes -- huge.txt (2000) should not be cached
	cache := newLruCacheFs(base, 100).(*lruCacheFs)

	f, err := cache.Open("huge.txt")
	require.NoError(t, err)
	data, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Len(t, data, 2000)
	f.Close()

	// should not be in cache
	require.Len(t, cache.entries, 0)
	require.Equal(t, int64(0), cache.curBytes)
}

func TestLruCacheFsDirectoryNotCached(t *testing.T) {
	base := newTestFs()
	cache := newLruCacheFs(base, 1024).(*lruCacheFs)

	f, err := cache.Open("dir")
	require.NoError(t, err)
	info, err := f.Stat()
	require.NoError(t, err)
	require.True(t, info.IsDir())
	f.Close()

	// directories should not be in cache
	require.Len(t, cache.entries, 0)
}

func TestLruCacheFsStat(t *testing.T) {
	base := newTestFs()
	cache := newLruCacheFs(base, 1024)

	// stat before any open -- should go to base
	info, err := cache.Stat("small.txt")
	require.NoError(t, err)
	require.Equal(t, int64(5), info.Size())
	require.Equal(t, "small.txt", info.Name())

	// open to populate cache
	f, _ := cache.Open("small.txt")
	f.Close()

	// stat from cache
	info, err = cache.Stat("small.txt")
	require.NoError(t, err)
	require.Equal(t, int64(5), info.Size())
	require.Equal(t, "small.txt", info.Name())
}

func TestLruCacheFsSeek(t *testing.T) {
	base := newTestFs()
	cache := newLruCacheFs(base, 1024)

	f, err := cache.Open("small.txt")
	require.NoError(t, err)

	// seek to offset 2
	pos, err := f.Seek(2, io.SeekStart)
	require.NoError(t, err)
	require.Equal(t, int64(2), pos)

	data, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, "llo", string(data))
	f.Close()
}

func TestLruCacheFsReadOnly(t *testing.T) {
	base := newTestFs()
	cache := newLruCacheFs(base, 1024)

	_, err := cache.Create("new.txt")
	require.Error(t, err)

	err = cache.Mkdir("newdir", 0o755)
	require.Error(t, err)

	err = cache.Remove("small.txt")
	require.Error(t, err)
}

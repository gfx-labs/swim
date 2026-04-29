package github_preview

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/afero"
)

// lruCacheFs wraps an afero.Fs and caches file contents in memory with
// LRU eviction by total cached bytes. files are cached on first read
// and served from memory on subsequent reads. the cache is safe for
// concurrent use.
type lruCacheFs struct {
	base     afero.Fs
	mu       sync.Mutex
	entries  map[string]*cacheEntry
	maxBytes int64
	curBytes int64
	// lru doubly-linked list
	head *cacheEntry // most recent
	tail *cacheEntry // least recent
}

type cacheEntry struct {
	name    string
	data    []byte
	size    int64
	modTime time.Time
	mode    os.FileMode
	// lru pointers
	prev *cacheEntry
	next *cacheEntry
}

func newLruCacheFs(base afero.Fs, maxBytes int64) afero.Fs {
	return &lruCacheFs{
		base:     base,
		entries:  make(map[string]*cacheEntry),
		maxBytes: maxBytes,
	}
}

func (c *lruCacheFs) Open(name string) (afero.File, error) {
	c.mu.Lock()
	if e, ok := c.entries[name]; ok {
		c.touchLocked(e)
		c.mu.Unlock()
		return newCachedFile(e), nil
	}
	c.mu.Unlock()

	// cache miss -- read from underlying fs
	f, err := c.base.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	// don't cache directories
	if info.IsDir() {
		// re-open from base since we closed f
		return c.base.Open(name)
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	e := &cacheEntry{
		name:    name,
		data:    data,
		size:    int64(len(data)),
		modTime: info.ModTime(),
		mode:    info.Mode(),
	}

	// skip caching if the file is larger than the entire cache
	if e.size > c.maxBytes {
		return newCachedFile(e), nil
	}

	c.mu.Lock()
	// check again in case another goroutine cached it
	if existing, ok := c.entries[name]; ok {
		c.touchLocked(existing)
		c.mu.Unlock()
		return newCachedFile(existing), nil
	}
	c.addLocked(e)
	c.mu.Unlock()

	return newCachedFile(e), nil
}

func (c *lruCacheFs) addLocked(e *cacheEntry) {
	// evict until we have room
	for c.curBytes+e.size > c.maxBytes && c.tail != nil {
		c.evictLocked(c.tail)
	}
	c.entries[e.name] = e
	c.curBytes += e.size
	// add to front of list
	e.prev = nil
	e.next = c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

func (c *lruCacheFs) touchLocked(e *cacheEntry) {
	if c.head == e {
		return
	}
	// unlink
	if e.prev != nil {
		e.prev.next = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	}
	if c.tail == e {
		c.tail = e.prev
	}
	// move to front
	e.prev = nil
	e.next = c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
}

func (c *lruCacheFs) evictLocked(e *cacheEntry) {
	// unlink
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	c.curBytes -= e.size
	delete(c.entries, e.name)
}

// delegate read-only operations to base

func (c *lruCacheFs) Stat(name string) (os.FileInfo, error) {
	c.mu.Lock()
	if e, ok := c.entries[name]; ok {
		c.mu.Unlock()
		return &cachedFileInfo{e}, nil
	}
	c.mu.Unlock()
	return c.base.Stat(name)
}

func (c *lruCacheFs) Name() string                            { return "lrucache" }
func (c *lruCacheFs) Create(name string) (afero.File, error)  { return nil, errReadOnly }
func (c *lruCacheFs) Mkdir(name string, perm os.FileMode) error { return errReadOnly }
func (c *lruCacheFs) MkdirAll(path string, perm os.FileMode) error { return errReadOnly }
func (c *lruCacheFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if flag != os.O_RDONLY {
		return nil, errReadOnly
	}
	return c.Open(name)
}
func (c *lruCacheFs) Remove(name string) error                { return errReadOnly }
func (c *lruCacheFs) RemoveAll(path string) error             { return errReadOnly }
func (c *lruCacheFs) Rename(oldname, newname string) error    { return errReadOnly }
func (c *lruCacheFs) Chmod(name string, mode os.FileMode) error { return errReadOnly }
func (c *lruCacheFs) Chown(name string, uid, gid int) error   { return errReadOnly }
func (c *lruCacheFs) Chtimes(name string, atime time.Time, mtime time.Time) error { return errReadOnly }

var errReadOnly = &os.PathError{Op: "write", Err: os.ErrPermission}

// cachedFile is an in-memory file backed by a cache entry
type cachedFile struct {
	entry  *cacheEntry
	reader *bytes.Reader
}

func newCachedFile(e *cacheEntry) afero.File {
	return &cachedFile{
		entry:  e,
		reader: bytes.NewReader(e.data),
	}
}

func (f *cachedFile) Name() string                                    { return f.entry.name }
func (f *cachedFile) Read(p []byte) (int, error)                      { return f.reader.Read(p) }
func (f *cachedFile) ReadAt(p []byte, off int64) (int, error)         { return f.reader.ReadAt(p, off) }
func (f *cachedFile) Seek(offset int64, whence int) (int64, error)    { return f.reader.Seek(offset, whence) }
func (f *cachedFile) Stat() (os.FileInfo, error)                      { return &cachedFileInfo{f.entry}, nil }
func (f *cachedFile) Close() error                                    { return nil }
func (f *cachedFile) Write(p []byte) (int, error)                     { return 0, errReadOnly }
func (f *cachedFile) WriteAt(p []byte, off int64) (int, error)        { return 0, errReadOnly }
func (f *cachedFile) WriteString(s string) (int, error)               { return 0, errReadOnly }
func (f *cachedFile) Truncate(size int64) error                       { return errReadOnly }
func (f *cachedFile) Sync() error                                     { return nil }
func (f *cachedFile) Readdir(count int) ([]os.FileInfo, error)        { return nil, nil }
func (f *cachedFile) Readdirnames(n int) ([]string, error)            { return nil, nil }

// cachedFileInfo implements os.FileInfo from a cache entry
type cachedFileInfo struct {
	entry *cacheEntry
}

func (fi *cachedFileInfo) Name() string      { return filepath.Base(fi.entry.name) }
func (fi *cachedFileInfo) Size() int64       { return fi.entry.size }
func (fi *cachedFileInfo) Mode() os.FileMode { return fi.entry.mode }
func (fi *cachedFileInfo) ModTime() time.Time { return fi.entry.modTime }
func (fi *cachedFileInfo) IsDir() bool       { return false }
func (fi *cachedFileInfo) Sys() any          { return nil }

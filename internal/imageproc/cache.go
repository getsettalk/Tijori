package imageproc

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cache stores processed images on disk and evicts the least recently used
// entries when the total size goes over the limit.
type Cache struct {
	dir   string
	limit int64

	mu      sync.Mutex
	entries map[string]*cacheEntry
	total   int64
	hits    int64
	misses  int64
}

type cacheEntry struct {
	size int64
	used time.Time
}

// NewCache opens (and indexes) a cache directory.
func NewCache(dir string, limit int64) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	c := &Cache{dir: dir, limit: limit, entries: map[string]*cacheEntry{}}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".tmp-") {
			os.Remove(p)
			return nil
		}
		if fi, err := d.Info(); err == nil {
			c.entries[name] = &cacheEntry{size: fi.Size(), used: fi.ModTime()}
			c.total += fi.Size()
		}
		return nil
	})
	c.mu.Lock()
	c.evictLocked()
	c.mu.Unlock()
	return c, nil
}

func (c *Cache) path(name string) string {
	return filepath.Join(c.dir, name[:2], name)
}

// Get returns a cached entry and marks it as recently used.
func (c *Cache) Get(name string) ([]byte, bool) {
	c.mu.Lock()
	e, ok := c.entries[name]
	if ok {
		e.used = time.Now()
		c.hits++
	} else {
		c.misses++
	}
	c.mu.Unlock()
	if !ok {
		return nil, false
	}
	b, err := os.ReadFile(c.path(name))
	if err != nil {
		c.mu.Lock()
		if e, ok := c.entries[name]; ok {
			c.total -= e.size
			delete(c.entries, name)
		}
		c.hits--
		c.misses++
		c.mu.Unlock()
		return nil, false
	}
	now := time.Now()
	_ = os.Chtimes(c.path(name), now, now) // remember recency across restarts
	return b, true
}

// Put stores an entry, evicting old ones if needed.
func (c *Cache) Put(name string, data []byte) {
	if c.limit > 0 && int64(len(data)) > c.limit/4 {
		return // never let one image take over the cache
	}
	p := c.path(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil || os.Rename(f.Name(), p) != nil {
		os.Remove(f.Name())
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[name]; ok {
		c.total -= old.size
	}
	c.entries[name] = &cacheEntry{size: int64(len(data)), used: time.Now()}
	c.total += int64(len(data))
	c.evictLocked()
}

func (c *Cache) evictLocked() {
	if c.limit <= 0 || c.total <= c.limit {
		return
	}
	type kv struct {
		name string
		e    *cacheEntry
	}
	all := make([]kv, 0, len(c.entries))
	for n, e := range c.entries {
		all = append(all, kv{n, e})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].e.used.Before(all[j].e.used) })
	target := c.limit * 9 / 10
	for _, x := range all {
		if c.total <= target {
			break
		}
		os.Remove(c.path(x.name))
		c.total -= x.e.size
		delete(c.entries, x.name)
	}
}

// Stats reports cache size and hit counts.
func (c *Cache) Stats() (entries int, bytes, hits, misses int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.total, c.hits, c.misses
}

// Clear deletes every cached image.
func (c *Cache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name := range c.entries {
		os.Remove(c.path(name))
	}
	c.entries = map[string]*cacheEntry{}
	c.total = 0
	return nil
}

package scratch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/kazupc/paritydesk/internal/fsx"
)

// FileStat is what a stat cache hit must match: device, inode, size,
// modification and change time.
type FileStat struct {
	Dev, Ino         uint64
	Size             int64
	MtimeNs, CtimeNs int64
}

// statCacheVersion is the format version of the cache file.
const statCacheVersion = 1

// maxStatCacheFile bounds the cache file read at load.
const maxStatCacheFile = 256 << 20

// cacheEntry is one cached hash.
type cacheEntry struct {
	Dev     uint64 `json:"dev"`
	Ino     uint64 `json:"ino"`
	Size    int64  `json:"size"`
	MtimeNs int64  `json:"mtime_ns"`
	CtimeNs int64  `json:"ctime_ns"`
	SHA256  string `json:"sha256"`
}

// cacheFile is the stored form.
type cacheFile struct {
	V       int                              `json:"v"`
	Folders map[string]map[string]cacheEntry `json:"folders"`
}

// StatCache remembers file hashes by (folder, path) and stat. A hit needs
// device, inode, size, mtime and ctime equal; ctime cannot be set by user
// tools, so a hit means the file was not written since it was hashed. It
// is safe for concurrent use.
type StatCache struct {
	mu      sync.Mutex
	loaded  map[string]map[string]cacheEntry
	touched map[string]map[string]cacheEntry
	dirty   bool
}

// NewStatCache returns an empty cache.
func NewStatCache() *StatCache {
	return &StatCache{loaded: map[string]map[string]cacheEntry{}, touched: map[string]map[string]cacheEntry{}}
}

// LoadStatCache never fails: an absent, unreadable or malformed file
// gives an empty cache.
func LoadStatCache(path string) *StatCache {
	c := NewStatCache()
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxStatCacheFile {
		return c
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var f cacheFile
	if json.Unmarshal(b, &f) != nil || f.V != statCacheVersion {
		return c
	}
	for folder, m := range f.Folders {
		if m != nil {
			c.loaded[folder] = m
		}
	}
	return c
}

// Lookup returns the cached hash of folderKey/path when every field of st
// matches. A hit keeps the entry for Save.
func (c *StatCache) Lookup(folderKey, path string, st FileStat) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.loaded[folderKey][path]
	if !ok {
		e, ok = c.touched[folderKey][path]
	}
	if !ok || e.Dev != st.Dev || e.Ino != st.Ino || e.Size != st.Size || e.MtimeNs != st.MtimeNs || e.CtimeNs != st.CtimeNs || e.SHA256 == "" {
		return "", false
	}
	c.keep(folderKey, path, e)
	return e.SHA256, true
}

// Put records the hash of folderKey/path at st.
func (c *StatCache) Put(folderKey, path string, st FileStat, sha string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keep(folderKey, path, cacheEntry{Dev: st.Dev, Ino: st.Ino, Size: st.Size, MtimeNs: st.MtimeNs, CtimeNs: st.CtimeNs, SHA256: sha})
	c.dirty = true
}

func (c *StatCache) keep(folderKey, path string, e cacheEntry) {
	if c.touched[folderKey] == nil {
		c.touched[folderKey] = map[string]cacheEntry{}
	}
	if old, ok := c.touched[folderKey][path]; !ok || old != e {
		c.touched[folderKey][path] = e
	}
}

// Dirty reports whether Save would write.
func (c *StatCache) Dirty() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dirty || c.dropsEntries()
}

// dropsEntries reports whether the loaded file holds entries this run did
// not touch (Save would drop them).
func (c *StatCache) dropsEntries() bool {
	for f, m := range c.loaded {
		for p := range m {
			if _, ok := c.touched[f][p]; !ok {
				return true
			}
		}
	}
	return false
}

// Len returns the number of entries Save would write.
func (c *StatCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, m := range c.touched {
		n += len(m)
	}
	return n
}

// Save writes the cache atomically (0600) through fs, keeping only the
// entries looked up or put since Load (bounded by what this run read). It
// creates the file's folder (0700) when it is missing, and writes nothing
// when nothing changed. A nil cache saves nothing.
func (c *StatCache) Save(o fsx.Ops, path string, tmpDir string) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty && !c.dropsEntries() {
		return nil
	}
	if o == nil {
		return errors.New("scratch: stat cache: no filesystem")
	}
	f := cacheFile{V: statCacheVersion, Folders: map[string]map[string]cacheEntry{}}
	folders := make([]string, 0, len(c.touched))
	for k := range c.touched {
		folders = append(folders, k)
	}
	sort.Strings(folders)
	for _, k := range folders {
		f.Folders[k] = c.touched[k]
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := o.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := o.WriteFileAtomic(filepath.Dir(path), filepath.Base(path), b, 0o600, tmpDir); err != nil {
		return err
	}
	c.loaded, c.dirty = c.touched, false
	c.touched = map[string]map[string]cacheEntry{}
	for k, m := range c.loaded {
		c.touched[k] = m
	}
	return nil
}

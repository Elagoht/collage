package cache

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrEmptyCacheDir is returned by NewDisk when no directory is configured.
var ErrEmptyCacheDir = errors.New("collage: disk cache needs a directory")

// ErrEmptyCacheVersion is returned by NewDisk when no version is configured.
//
// A disk cache outlives the process that filled it, so without a version a new
// binary serves HTML the old one rendered — a changed template, a changed data
// handler, and the page nobody can explain. The version is what makes that
// impossible rather than unlikely: entries live under a directory named for it, so
// a different version reads a different directory and finds nothing.
var ErrEmptyCacheVersion = errors.New("collage: disk cache needs a version")

// evictBatch sets how far below MaxEntries a cap-triggered eviction removes: down
// to maxEntries - maxEntries/evictBatch. Removing a batch rather than a single
// entry amortises the directory scan over that many later inserts.
const evictBatch = 16

// DiskConfig configures a DiskCache.
type DiskConfig struct {
	// Dir is the directory entries are stored under. Required.
	Dir string
	// Version identifies the build whose output this cache holds. Required.
	//
	// Anything that changes when the rendered output could change: a git commit, a
	// release tag, a build timestamp. Entries are stored under a subdirectory named
	// for a hash of it, so changing it starts a fresh cache and leaves the old one
	// to be swept.
	Version string
	// DefaultTTL is the TTL used when Set is called with zero or less. Defaults to
	// five minutes.
	DefaultTTL time.Duration
	// MaxEntries caps the number of stored entries, oldest evicted first by file
	// modification time. Zero means "use the default" (10000); a negative value
	// means unlimited, which a disk cache open to anonymous writes should set only
	// deliberately — the cache key includes the request's Host and, by default, its
	// whole query, so without a cap an anonymous caller choosing those fills the
	// disk one never-evicted file at a time.
	MaxEntries int
	// Now overrides the clock, for tests.
	Now func() time.Time
}

// DiskCache stores rendered output in files, so it survives a restart.
//
// One file per entry, holding a JSON header line and then the content. One file
// rather than a pair, because two files are two chances to see half a write; one
// line of JSON rather than a sidecar, because the header is small and reading it
// costs one buffered read rather than a second open.
//
// There is no index of tags. Invalidate reads the header of every entry instead,
// which is O(entries) small reads on a call that happens when content is published
// — rare, and paid in exchange for having no second structure that can disagree
// with the first or be left behind by a crash.
type DiskCache struct {
	dir        string
	defaultTTL time.Duration
	maxEntries int
	now        func() time.Time

	// mu serialises writes and removals. Reads go to the filesystem directly: an
	// entry file is written atomically, so a reader sees either the whole previous
	// entry or the whole new one.
	mu sync.Mutex
	// entries is an in-memory count of stored files, kept only to decide when to
	// scan for eviction. It is approximate — an invalidation does not adjust it —
	// but evictIfNeeded recounts from disk before removing anything, so an
	// inflated count costs at worst a wasted scan, never an over-eviction.
	entries int
}

// header is the JSON line at the front of an entry file.
type header struct {
	ETag      string   `json:"etag"`
	ExpiresAt int64    `json:"expiresAt"`
	Tags      []string `json:"tags,omitempty"`
}

var _ TaggedCache = (*DiskCache)(nil)

// NewDisk returns a DiskCache under cfg.Dir, in a subdirectory named for cfg.Version.
func NewDisk(cfg DiskConfig) (*DiskCache, error) {
	if cfg.Dir == "" {
		return nil, ErrEmptyCacheDir
	}
	if cfg.Version == "" {
		return nil, ErrEmptyCacheVersion
	}
	if cfg.DefaultTTL <= 0 {
		cfg.DefaultTTL = 5 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = defaultMaxEntries
	}

	sum := sha256.Sum256([]byte(cfg.Version))
	dir := filepath.Join(cfg.Dir, hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("collage: disk cache directory %s: %w", dir, err)
	}

	c := &DiskCache{dir: dir, defaultTTL: cfg.DefaultTTL, maxEntries: cfg.MaxEntries, now: cfg.Now}
	// Count what a previous process left here, so the cap holds across restarts
	// rather than only within one run. Best-effort: an unreadable directory starts
	// the count at zero and the first eviction scan corrects it.
	c.entries = c.countEntries()
	return c, nil
}

// countEntries returns how many stored entry files the directory holds, ignoring
// temporary files, dotfiles and subdirectories — the same set Invalidate walks.
func (c *DiskCache) countEntries() int {
	dirEntries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, entry := range dirEntries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		n++
	}
	return n
}

// Dir is where this cache stores its entries, version subdirectory included.
func (c *DiskCache) Dir() string { return c.dir }

// Get returns the cached content and ETag for key.
//
// An expired entry is reported missing and deleted on the way out, so a cache that
// is read but never written does not grow without bound.
func (c *DiskCache) Get(_ context.Context, key string) ([]byte, string, bool) {
	path, ok := c.path(key)
	if !ok {
		return nil, "", false
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, "", false
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, "", false
	}
	var h header
	if err := json.Unmarshal(line, &h); err != nil {
		return nil, "", false
	}
	if c.now().UnixNano() >= h.ExpiresAt {
		c.mu.Lock()
		os.Remove(path)
		c.mu.Unlock()
		return nil, "", false
	}

	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, "", false
	}
	return content, h.ETag, true
}

// Set stores content under key.
func (c *DiskCache) Set(ctx context.Context, key string, content []byte, ttl time.Duration) (string, error) {
	return c.SetTagged(ctx, key, content, ttl, nil)
}

// SetTagged stores content under key with tags.
func (c *DiskCache) SetTagged(_ context.Context, key string, content []byte, ttl time.Duration, tags []string) (string, error) {
	path, ok := c.path(key)
	if !ok {
		return "", fmt.Errorf("collage: disk cache: unusable key %q", key)
	}
	if ttl <= 0 {
		ttl = c.defaultTTL
	}

	etag := ETag(content)
	line, err := json.Marshal(header{
		ETag:      etag,
		ExpiresAt: c.now().Add(ttl).UnixNano(),
		Tags:      tags,
	})
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	buf.Grow(len(line) + 1 + len(content))
	buf.Write(line)
	buf.WriteByte('\n')
	buf.Write(content)

	c.mu.Lock()
	defer c.mu.Unlock()
	// Whether this key is already stored decides whether the write adds an entry
	// or replaces one, and so whether it counts toward the cap.
	_, statErr := os.Stat(path)
	isNew := errors.Is(statErr, fs.ErrNotExist)
	if err := c.writeAtomic(path, buf.Bytes()); err != nil {
		return "", err
	}
	if isNew {
		c.entries++
		c.evictIfNeeded()
	}
	return etag, nil
}

// evictIfNeeded removes the oldest entries, by file modification time, once the
// stored count passes maxEntries. Called with c.mu held. It recounts from disk
// before removing, so it never over-evicts on an inflated in-memory count, and it
// removes down to a fraction below the cap so the scan is paid once per many
// inserts rather than on every insert at a full cache.
func (c *DiskCache) evictIfNeeded() {
	if c.maxEntries <= 0 || c.entries <= c.maxEntries {
		return
	}

	dirEntries, err := os.ReadDir(c.dir)
	if err != nil {
		return // best-effort: a cap is not worth failing a write over
	}
	type stored struct {
		name    string
		modTime time.Time
	}
	files := make([]stored, 0, len(dirEntries))
	for _, entry := range dirEntries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, stored{entry.Name(), info.ModTime()})
	}

	c.entries = len(files)
	if c.entries <= c.maxEntries {
		return // the in-memory count was stale; nothing to do
	}

	// Down to a batch below the cap, so the next scan is a batch of inserts away.
	target := c.maxEntries - c.maxEntries/evictBatch
	if target < 1 {
		target = 1
	}
	remove := c.entries - target
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	for i := 0; i < remove && i < len(files); i++ {
		if err := os.Remove(filepath.Join(c.dir, files[i].name)); err == nil || errors.Is(err, fs.ErrNotExist) {
			c.entries--
		}
	}
}

// Invalidate removes every entry carrying any of tags.
func (c *DiskCache) Invalidate(_ context.Context, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		wanted[tag] = struct{}{}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return fmt.Errorf("collage: disk cache: list %s: %w", c.dir, err)
	}

	var failures []error
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(c.dir, entry.Name())
		h, err := readHeader(path)
		if err != nil {
			// An unreadable entry is a broken entry. Removing it is the same
			// outcome a caller asking for invalidation wants, and leaving it would
			// mean carrying a file nothing can ever read or expire.
			os.Remove(path)
			continue
		}
		for _, tag := range h.Tags {
			if _, hit := wanted[tag]; hit {
				if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					failures = append(failures, err)
				}
				break
			}
		}
	}
	return errors.Join(failures...)
}

// InvalidateKey removes the entry stored under key.
func (c *DiskCache) InvalidateKey(_ context.Context, key string) error {
	path, ok := c.path(key)
	if !ok {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Clear removes every entry.
func (c *DiskCache) Clear(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = 0

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(c.dir, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// path is the file an entry is stored at.
//
// The key is hashed rather than used directly. Keys already arrive as hex from
// cache.Key, but Cache is a public interface and a caller can store under anything;
// hashing means no key can name a path, a parent directory, or a filename longer
// than the filesystem accepts.
func (c *DiskCache) path(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:])), true
}

// writeAtomic writes through a temporary file and a rename, so a reader sees either
// the whole previous entry or the whole new one. Called with c.mu held.
func (c *DiskCache) writeAtomic(path string, body []byte) error {
	tmp, err := os.CreateTemp(c.dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// readHeader reads an entry's header without reading its content.
func readHeader(path string) (header, error) {
	file, err := os.Open(path)
	if err != nil {
		return header{}, err
	}
	defer file.Close()

	line, err := bufio.NewReader(file).ReadBytes('\n')
	if err != nil {
		return header{}, err
	}
	var h header
	if err := json.Unmarshal(line, &h); err != nil {
		return header{}, err
	}
	return h, nil
}

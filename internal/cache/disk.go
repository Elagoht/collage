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

// DefaultDiskMaxBytes is the size DiskConfig.MaxBytes caps a disk cache at when
// left at zero: 1 GiB.
const DefaultDiskMaxBytes int64 = 1 << 30

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
	//
	// Eviction scans the directory without blocking other writers. A new entry
	// that arrives while the cache is full and a scan is still making room is not
	// stored: the page is served, and only not kept.
	MaxEntries int
	// MaxBytes caps the total size of the entry files, in bytes. Zero means "use
	// the default" (DefaultDiskMaxBytes); a negative value means unlimited. It is
	// enforced the way MaxEntries is, oldest first and by the same scan, and an
	// entry larger than the whole cap is not stored. MaxEntries counts files, not
	// what they weigh: a large page under thousands of invented queries fills the
	// disk with copies of it.
	MaxBytes int64
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
	maxBytes   int64
	now        func() time.Time

	// mu serialises renaming entries into place, removals, and the count. Reads
	// go to the filesystem directly: an entry file is written atomically, so a
	// reader sees either the whole previous entry or the whole new one.
	mu sync.Mutex
	// entries is an in-memory count of stored files, kept only to decide when to
	// scan for eviction. It is approximate — an invalidation does not adjust it —
	// but evict recounts from disk before removing anything, so an
	// inflated count costs at worst a wasted scan, never an over-eviction.
	entries int
	// bytes is the same kind of count of the entry files' total size: inflated
	// by an invalidation, recounted by every eviction scan.
	bytes int64

	// evicting is set while an eviction scan runs, outside mu.
	evicting bool
	// scanned, when set, runs after an eviction's directory scan; tests use it to
	// hold a scan open.
	scanned func()
}

// header is the JSON line at the front of an entry file.
type header struct {
	// Key is the cache key the entry was stored under. File names are hashes of
	// it and cannot be turned back into it, so this is what lets a tiered cache
	// warm its memory tier from the directory after a restart. Files written
	// before it existed lack it; they are still read by key, only not warmed.
	Key       string   `json:"key,omitempty"`
	ETag      string   `json:"etag"`
	ExpiresAt int64    `json:"expiresAt"`
	Tags      []string `json:"tags,omitempty"`
	// Stale is set by MarkStaleKey and MarkStale, so a mark survives a restart
	// along with the entry. omitempty keeps a fresh entry's file as it was.
	Stale bool `json:"stale,omitempty"`
}

var (
	_ Cache       = (*DiskCache)(nil)
	_ TaggedCache = (*DiskCache)(nil)
	_ StaleCache  = (*DiskCache)(nil)
)

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
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = DefaultDiskMaxBytes
	}

	sum := sha256.Sum256([]byte(cfg.Version))
	dir := filepath.Join(cfg.Dir, hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("collage: disk cache directory %s: %w", dir, err)
	}

	c := &DiskCache{dir: dir, defaultTTL: cfg.DefaultTTL, maxEntries: cfg.MaxEntries, maxBytes: cfg.MaxBytes, now: cfg.Now}
	// Count what a previous process left here, so the cap holds across restarts
	// rather than only within one run. Best-effort: an unreadable directory starts
	// the count at zero and the first eviction scan corrects it.
	c.entries = c.countEntries()
	c.bytes = c.storedBytes()
	return c, nil
}

// storedBytes returns the total size of the stored entry files: the set
// countEntries counts.
func (c *DiskCache) storedBytes() int64 {
	dirEntries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, entry := range dirEntries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if info, err := entry.Info(); err == nil {
			n += info.Size()
		}
	}
	return n
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

// Get returns the cached content and ETag for key. An entry that has expired or
// been marked stale is reported missing but left on disk for GetStale; eviction,
// not reading, is what bounds the directory.
func (c *DiskCache) Get(ctx context.Context, key string) ([]byte, string, bool) {
	content, etag, stale, found := c.GetStale(ctx, key)
	if !found || stale {
		return nil, "", false
	}
	return content, etag, true
}

// GetStale returns key's entry whether it is fresh or not; stale is true when it
// has expired or been marked stale.
func (c *DiskCache) GetStale(_ context.Context, key string) ([]byte, string, bool, bool) {
	path, ok := c.path(key)
	if !ok {
		return nil, "", false, false
	}
	h, content, err := readEntry(path)
	if err != nil {
		return nil, "", false, false
	}
	return content, h.ETag, h.Stale || c.now().UnixNano() >= h.ExpiresAt, true
}

// load returns key's whole entry — content, ETag, expiry and tags — and whether it
// is stale, for a tiered cache copying it into memory as it is. found is false for
// a missing or unreadable entry.
func (c *DiskCache) load(key string) (Entry, bool, bool) {
	path, ok := c.path(key)
	if !ok {
		return Entry{}, false, false
	}
	h, content, err := readEntry(path)
	if err != nil {
		return Entry{}, false, false
	}
	e, stale := c.entryOf(h, content)
	return e, stale, true
}

// entryOf turns a stored header and content into an Entry, and reports whether
// it is stale by the same rule GetStale applies.
func (c *DiskCache) entryOf(h header, content []byte) (Entry, bool) {
	var expiresAt time.Time
	if h.ExpiresAt != 0 {
		expiresAt = time.Unix(0, h.ExpiresAt)
	}
	e := Entry{Content: content, ETag: h.ETag, ExpiresAt: expiresAt, Tags: h.Tags}
	return e, h.Stale || c.now().UnixNano() >= h.ExpiresAt
}

// newestFirst returns the paths of the stored entry files, most recently written
// first, skipping the temporary files, dotfiles and subdirectories countEntries
// skips. It returns paths rather than keys because a file name is a hash of its
// key; the key is in the header. An unreadable directory yields none.
func (c *DiskCache) newestFirst() []string {
	dirEntries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil
	}
	type stored struct {
		path    string
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
		files = append(files, stored{filepath.Join(c.dir, entry.Name()), info.ModTime()})
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.path
	}
	return paths
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
		Key:       key,
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

	size := int64(buf.Len())
	// Whether this key is already stored, and how large it was, decides what the
	// write adds to the counts the caps are checked against. Read outside the
	// lock, so a racing writer of the same key can count it twice: the counts
	// are approximate already, and the eviction scan recounts.
	info, statErr := os.Stat(path)
	isNew := errors.Is(statErr, fs.ErrNotExist)
	var oldSize int64
	if statErr == nil {
		oldSize = info.Size()
	}

	if c.maxBytes > 0 && size > c.maxBytes {
		// Larger than the whole cache: storing it would evict everything else
		// and then itself. Served, not kept; the entry it replaces is stale now.
		c.mu.Lock()
		if err := os.Remove(path); err == nil {
			c.entries--
			c.bytes -= oldSize
		}
		c.mu.Unlock()
		return etag, nil
	}

	// The file is written before the lock and only renamed into place under it:
	// writing is the slow part, and two writers of different keys have nothing
	// to wait on each other for.
	tmp, err := c.writeTemp(buf.Bytes())
	if err != nil {
		return "", err
	}

	grows := size - oldSize
	c.mu.Lock()
	if c.evicting && ((isNew && c.maxEntries > 0 && c.entries >= c.maxEntries) ||
		(grows > 0 && c.maxBytes > 0 && c.bytes+grows > c.maxBytes)) {
		// Full, and a scan is already making room. Storing this entry would
		// pass a cap; waiting for the scan would make every writer queue
		// behind it. The render is served either way and only not kept.
		c.mu.Unlock()
		os.Remove(tmp)
		return etag, nil
	}
	if err := os.Rename(tmp, path); err != nil {
		c.mu.Unlock()
		os.Remove(tmp)
		if errors.Is(err, fs.ErrNotExist) {
			return etag, nil // a Clear took the file between write and rename
		}
		return "", err
	}
	if isNew {
		c.entries++
	}
	c.bytes += grows
	var start int
	var startBytes int64
	evict := false
	if !c.evicting && c.overEvictAt(c.entries, c.bytes) {
		c.evicting, evict, start, startBytes = true, true, c.entries, c.bytes
	}
	c.mu.Unlock()
	if evict {
		c.evict(start, startBytes)
	}
	return etag, nil
}

// overEvictAt reports whether entries files of n bytes in all are past the point
// a write starts an eviction: a little short of each cap, so writes go on being
// stored while the scan runs. A cap too small for that margin evicts once it is
// passed itself. A cap that is off is never passed.
func (c *DiskCache) overEvictAt(entries int, n int64) bool {
	return (c.maxEntries > 0 && entries > c.maxEntries-c.maxEntries/(2*evictBatch)) ||
		(c.maxBytes > 0 && n > c.maxBytes-c.maxBytes/(2*evictBatch))
}

// evict removes the oldest entries, by file modification time, down to a batch
// below maxEntries and maxBytes. It runs without c.mu, in the write that crossed evictAt, so
// the directory scan stalls that one write and no other; c.evicting keeps a second
// scan from starting meanwhile, and writes of new entries that reach maxEntries
// before this one finishes are skipped rather than stored past it. start and
// startBytes are the in-memory counts when the scan began.
//
// It recounts from disk before removing, so it never over-evicts on an inflated
// in-memory count, and it removes down to a fraction below the cap so the scan is
// paid once per many inserts rather than on every insert at a full cache.
func (c *DiskCache) evict(start int, startBytes int64) {
	removed, scanned := 0, -1
	var removedBytes, scannedBytes int64
	defer func() {
		c.mu.Lock()
		if scanned >= 0 {
			// What the scan saw, less what it removed, plus what was written
			// since it began. Entries written during the scan may be counted
			// twice; an inflated count costs an early scan, never a lost entry.
			c.entries = scanned - removed + (c.entries - start)
			c.bytes = scannedBytes - removedBytes + (c.bytes - startBytes)
		}
		c.evicting = false
		c.mu.Unlock()
	}()

	dirEntries, err := os.ReadDir(c.dir)
	if err != nil {
		return // best-effort: a cap is not worth failing a write over
	}
	type stored struct {
		name    string
		modTime time.Time
		size    int64
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
		files = append(files, stored{entry.Name(), info.ModTime(), info.Size()})
		scannedBytes += info.Size()
	}
	if c.scanned != nil {
		c.scanned()
	}
	scanned = len(files)
	if !c.overEvictAt(scanned, scannedBytes) {
		return // the in-memory counts were stale; nothing to do
	}

	// Down to a batch below each cap, so the next scan is a batch of inserts away.
	target := max(c.maxEntries-c.maxEntries/evictBatch, 1)
	targetBytes := c.maxBytes - c.maxBytes/evictBatch
	over := func() bool {
		return (c.maxEntries > 0 && scanned-removed > target) ||
			(c.maxBytes > 0 && scannedBytes-removedBytes > targetBytes)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	for i := 0; i < len(files) && over(); i++ {
		if err := os.Remove(filepath.Join(c.dir, files[i].name)); err == nil || errors.Is(err, fs.ErrNotExist) {
			removed++
			removedBytes += files[i].size
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

// MarkStaleKey marks the entry stored under key stale. A key with no entry is not
// an error.
func (c *DiskCache) MarkStaleKey(_ context.Context, key string) error {
	path, ok := c.path(key)
	if !ok {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.markStaleLocked(path)
}

// MarkStale marks every entry carrying any of tags stale. Like Invalidate, it
// reads every entry's header, and removes an entry whose header is corrupt. One it
// cannot read for another reason is left as it is, and the error returned.
func (c *DiskCache) MarkStale(_ context.Context, tags []string) error {
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
		switch {
		case errors.Is(err, errCorruptEntry):
			// Broken, as in Invalidate: nothing can ever read or expire it.
			os.Remove(path)
			continue
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			// The system's failure, not the file's: the page may be whole, and
			// removing it would lose what a failed render falls back to.
			failures = append(failures, fmt.Errorf("collage: disk cache: read %s: %w", path, err))
			continue
		}
		if h.Stale {
			// Already marked: nothing to rewrite, so no reason to read the rest.
			continue
		}
		for _, tag := range h.Tags {
			if _, hit := wanted[tag]; hit {
				if err := c.markStaleLocked(path); err != nil {
					failures = append(failures, err)
				}
				break
			}
		}
	}
	return errors.Join(failures...)
}

// markStaleLocked rewrites the entry at path with its header marked stale and
// everything else, content included, unchanged. A missing file is not an error, a
// corrupt one is removed, one that cannot be read for another reason is left and
// the error returned, and one already stale is left alone. Callers must hold c.mu,
// which keeps a SetTagged of the same key from being renamed into place between
// the read and the rename here.
//
// Eviction scans without c.mu, so it can remove the file between the read and the
// rename, and the rename then brings it back, once, marked stale. That is bounded:
// the next scan recounts the directory from disk, and the file, keeping its old
// modification time, is among the first it removes.
//
// The rewrite keeps the file's modification time. Eviction removes the oldest
// file first by that time, so a mark that made the file look new would evict
// fresh pages ahead of the stale one.
func (c *DiskCache) markStaleLocked(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	h, content, err := readEntry(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if errors.Is(err, errCorruptEntry) {
		os.Remove(path)
		return nil
	}
	if err != nil {
		return fmt.Errorf("collage: disk cache: read %s: %w", path, err)
	}
	if h.Stale {
		return nil
	}

	h.Stale = true
	line, err := json.Marshal(h)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.Grow(len(line) + 1 + len(content))
	buf.Write(line)
	buf.WriteByte('\n')
	buf.Write(content)

	tmp, err := c.writeTemp(buf.Bytes())
	if err != nil {
		return err
	}
	mod := info.ModTime()
	if err := os.Chtimes(tmp, mod, mod); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	c.bytes += int64(buf.Len()) - info.Size()
	return nil
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
	c.bytes = 0

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

// writeTemp writes body to a new temporary file in the cache directory and
// returns its name. The caller renames it into place: a rename within one
// directory is atomic, so a reader sees either the whole previous entry or the
// whole new one, never half a write.
func (c *DiskCache) writeTemp(body []byte) (string, error) {
	tmp, err := os.CreateTemp(c.dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// errCorruptEntry marks a file whose bytes are not an entry: a header cut off
// before its newline, or one that is not the JSON a header is. It is the one read
// failure that says the file itself is broken; any other — a permission, too many
// open files, an I/O error — is the system's, and says nothing about the file.
var errCorruptEntry = errors.New("collage: disk cache: corrupt entry")

// readEntry reads an entry's header and content. A file that is not an entry
// fails with an error wrapping errCorruptEntry.
func readEntry(path string) (header, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return header{}, nil, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	h, err := parseHeader(reader)
	if err != nil {
		return header{}, nil, err
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		return header{}, nil, err
	}
	return h, content, nil
}

// readHeader reads an entry's header without reading its content. A file that is
// not an entry fails with an error wrapping errCorruptEntry.
func readHeader(path string) (header, error) {
	file, err := os.Open(path)
	if err != nil {
		return header{}, err
	}
	defer file.Close()
	return parseHeader(bufio.NewReader(file))
}

// parseHeader reads the header line from reader.
func parseHeader(reader *bufio.Reader) (header, error) {
	line, err := reader.ReadBytes('\n')
	if errors.Is(err, io.EOF) {
		return header{}, fmt.Errorf("%w: no header line", errCorruptEntry)
	}
	if err != nil {
		return header{}, err
	}
	var h header
	if err := json.Unmarshal(line, &h); err != nil {
		return header{}, fmt.Errorf("%w: %w", errCorruptEntry, err)
	}
	return h, nil
}

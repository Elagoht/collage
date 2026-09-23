package asset

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"sync"
)

// etagCache memoises content-hash ETags per path. It stores hashes, never bodies,
// so its footprint is bounded by file count rather than by total asset size.
//
// A content hash is used rather than size and modification time because embed.FS
// reports a zero ModTime for every file, which would silently disable
// modification-time validation for the most common asset source.
type etagCache struct {
	mu   sync.RWMutex
	tags map[string]string
}

func newETagCache() *etagCache {
	return &etagCache{tags: make(map[string]string)}
}

// get returns the quoted strong ETag for name within fsys, hashing the file on
// first use.
func (c *etagCache) get(fsys fs.FS, name string) (string, error) {
	return c.lookup(fsys, name, true)
}

// lookup returns the tag for name, consulting and filling the memo only when
// memoise is set. A development-mode mount passes false: the file it names is one
// somebody is editing, and a remembered hash is a hash that stopped describing it.
func (c *etagCache) lookup(fsys fs.FS, name string, memoise bool) (string, error) {
	if memoise {
		c.mu.RLock()
		tag, ok := c.tags[name]
		c.mu.RUnlock()
		if ok {
			return tag, nil
		}
	}
	var tag string

	file, err := fsys.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	tag = `"` + hex.EncodeToString(sum.Sum(nil)[:16]) + `"`

	if memoise {
		c.mu.Lock()
		c.tags[name] = tag
		c.mu.Unlock()
	}
	return tag, nil
}

func (c *etagCache) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.tags)
}

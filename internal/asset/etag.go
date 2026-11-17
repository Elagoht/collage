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
	// inflight holds the hash being computed for a name, so clients that arrive
	// while it is wait for that answer instead of each reading the whole file: a
	// large cold asset requested by a crowd at once is one hash, not one per client.
	inflight map[string]*hashing
}

// hashing is one hash in progress. done closes once tag and err are set.
type hashing struct {
	done chan struct{}
	tag  string
	err  error
}

func newETagCache() *etagCache {
	return &etagCache{tags: make(map[string]string), inflight: make(map[string]*hashing)}
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
	if !memoise {
		return hashFile(fsys, name)
	}

	c.mu.RLock()
	tag, ok := c.tags[name]
	c.mu.RUnlock()
	if ok {
		return tag, nil
	}

	c.mu.Lock()
	if tag, ok := c.tags[name]; ok {
		c.mu.Unlock()
		return tag, nil
	}
	if pending, ok := c.inflight[name]; ok {
		c.mu.Unlock()
		<-pending.done
		return pending.tag, pending.err
	}
	pending := &hashing{done: make(chan struct{})}
	c.inflight[name] = pending
	c.mu.Unlock()

	pending.tag, pending.err = hashFile(fsys, name)

	c.mu.Lock()
	delete(c.inflight, name)
	// A failure is shared with whoever waited and then forgotten, so the next
	// request tries again: a file that was briefly unreadable is not
	// remembered as broken.
	if pending.err == nil {
		c.tags[name] = pending.tag
	}
	c.mu.Unlock()
	close(pending.done)
	return pending.tag, pending.err
}

// hashFile returns the quoted strong ETag of name's content.
func hashFile(fsys fs.FS, name string) (string, error) {
	file, err := fsys.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return `"` + hex.EncodeToString(sum.Sum(nil)[:16]) + `"`, nil
}

func (c *etagCache) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.tags)
}

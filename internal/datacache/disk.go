package datacache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// formatVersion is written into every file; a file of any other version is
// removed at startup rather than guessed at.
const formatVersion = 1

// header is the first line of an entry's file. The encoded value follows it.
type header struct {
	Version    int       `json:"v"`
	Key        string    `json:"key"`
	Tags       []string  `json:"tags,omitempty"`
	Expires    time.Time `json:"expires,omitzero"`
	Stale      bool      `json:"stale,omitempty"`
	StaleSince time.Time `json:"staleSince,omitzero"`
	Written    time.Time `json:"written"`
}

type record struct {
	header  header
	payload []byte
}

var errCorrupt = errors.New("not a data cache entry")

// disk keeps one file per entry in dir, named by the hash of its key. It is
// not namespaced by build: data is not a build's output, and a new deploy
// with its upstream down should still render from yesterday's.
type disk struct{ dir string }

func (d *disk) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(d.dir, hex.EncodeToString(sum[:]))
}

// write replaces key's file atomically: a reader, or a crash, sees the old
// file or the new one, never half of either.
func (d *disk) write(h header, payload []byte) error {
	line, err := json.Marshal(h)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d.dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(append(append(line, '\n'), payload...))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, d.path(h.Key))
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

func (d *disk) remove(key string) error {
	if err := os.Remove(d.path(key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// load reads every entry in the directory, newest first. Leftover temporary
// files and entry files that do not parse are removed. Only those: the
// directory sits inside the application's Cache.Dir, where anything else is
// someone else's, and a cache has no business deleting it.
func (d *disk) load() ([]record, error) {
	dirEntries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, err
	}
	var records []record
	for _, dirEntry := range dirEntries {
		if dirEntry.IsDir() {
			continue
		}
		name := dirEntry.Name()
		path := filepath.Join(d.dir, name)
		if strings.HasPrefix(name, ".tmp-") {
			_ = os.Remove(path)
			continue
		}
		if !isEntryName(name) {
			continue
		}
		rec, err := readRecord(path)
		if err != nil || d.path(rec.header.Key) != path {
			_ = os.Remove(path)
			continue
		}
		records = append(records, rec)
	}
	slices.SortFunc(records, func(a, b record) int { return b.header.Written.Compare(a.header.Written) })
	return records, nil
}

// isEntryName reports whether name is one path gives an entry: a SHA-256 in
// lower-case hex.
func isEntryName(name string) bool {
	if len(name) != sha256.Size*2 {
		return false
	}
	for _, c := range name {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

func readRecord(path string) (record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return record{}, err
	}
	line, payload, ok := bytes.Cut(data, []byte{'\n'})
	if !ok {
		return record{}, errCorrupt
	}
	var h header
	if err := json.Unmarshal(line, &h); err != nil || h.Version != formatVersion || h.Key == "" {
		return record{}, fmt.Errorf("%w: %s", errCorrupt, filepath.Base(path))
	}
	return record{header: h, payload: payload}, nil
}

package cli

import (
	"io/fs"
	"maps"
	"path/filepath"
	"strings"
	"time"
)

// watchInterval is how often "collage dev" looks for changed sources. A
// variable so the tests can shorten it.
var watchInterval = 300 * time.Millisecond

// stamp is what a watched file is compared by. Modification time and size
// rather than content: reading every source file several times a second would
// cost more than the rebuild it saves.
type stamp struct {
	modTime time.Time
	size    int64
}

// sources maps each watched file under a project to its stamp.
type sources map[string]stamp

// watchedFile reports whether a change to the file at rel — relative to the
// project root, with forward slashes — changes what "collage dev" runs.
//
// Only what the compiled program is made of, and the environment it is started
// with. Templates and static files are read from disk on every request in
// development, so a change to one needs no rebuild; a test file is not in the
// binary; and anything the running program writes — its cache, an export — is
// not a source at all. Watching those would rebuild on the program's own
// output, which is a loop.
func watchedFile(rel string) bool {
	name := filepath.Base(rel)
	switch {
	case strings.HasSuffix(name, "_test.go"):
		return false
	case strings.HasSuffix(name, ".go"):
		return true
	case name == "go.mod" || name == "go.sum":
		return !strings.Contains(rel, "/")
	}
	for _, env := range devEnvFiles {
		if rel == env {
			return true
		}
	}
	return false
}

// skippedDir reports whether the directory named name is never walked: hidden
// directories (.git, .cache), what "collage build" and "collage export" write
// (bin, dist), and trees that are not this program's source.
func skippedDir(name string) bool {
	if strings.HasPrefix(name, ".") && name != "." {
		return true
	}
	switch name {
	case "bin", "dist", "node_modules", "testdata", "vendor":
		return true
	}
	return false
}

// snapshotSources stamps every watched file under root.
func snapshotSources(root string) (sources, error) {
	snapshot := make(sources)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A file removed mid-walk is a change the next walk sees.
			return nil
		}
		if d.IsDir() {
			if path != root && skippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !watchedFile(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		snapshot[rel] = stamp{modTime: info.ModTime(), size: info.Size()}
		return nil
	})
	return snapshot, err
}

// equal reports whether two snapshots stamp the same files identically.
func (s sources) equal(other sources) bool {
	return maps.Equal(s, other)
}

package template

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
)

// HTMLConfig configures an HTMLEngine.
type HTMLConfig struct {
	// FS, when non-nil, is the filesystem templates are loaded from, and Root is
	// interpreted as a directory *within* it rather than as a path on disk. This is
	// what lets a binary embed its templates with //go:embed and run from any
	// working directory. When FS is nil, templates are loaded from the disk
	// directory named by Root.
	FS fs.FS
	// Root is the directory templates are loaded from: a path on disk when FS is
	// nil, otherwise a slash-separated path within FS. Template names are relative
	// to it, so Root is stripped from every name. When FS is non-nil, an empty Root
	// means the root of FS itself.
	Root string
	// Extension is the file extension, including the leading dot (e.g. ".html"),
	// that identifies template files under Root. Files with any other extension are
	// ignored.
	Extension string
	// DevMode, when true, makes Render and RenderWithFuncs reload every template
	// from disk before rendering. When false, templates are parsed once at
	// construction and only change on an explicit call to Reload.
	DevMode bool
	// Funcs overlays additional functions, or replacements for DefaultFuncs entries,
	// onto every template at parse time.
	Funcs template.FuncMap
}

// HTMLEngine is an Engine backed by the standard library's html/template package. It
// parses every template under HTMLConfig.Root into a single template set, so
// templates can reference each other by name with {{template "other/file.html" .}}.
type HTMLEngine struct {
	cfg HTMLConfig

	mu    sync.RWMutex
	tmpl  *template.Template
	names []string
}

var _ Engine = (*HTMLEngine)(nil)

// NewHTML constructs an HTMLEngine from cfg by walking cfg.Root and parsing every
// file whose extension matches cfg.Extension. It returns ErrTemplateRootMissing,
// wrapped with cfg.Root, if the root directory does not exist, and a wrapped parse
// error if any template fails to parse.
func NewHTML(cfg HTMLConfig) (*HTMLEngine, error) {
	e := &HTMLEngine{cfg: cfg}
	if err := e.Reload(); err != nil {
		return nil, err
	}
	return e, nil
}

// Render executes the template at path with data and writes it to w. It is exactly
// RenderWithFuncs(ctx, w, path, data, nil).
func (e *HTMLEngine) Render(ctx context.Context, w io.Writer, path string, data any) error { // any: template data
	return e.RenderWithFuncs(ctx, w, path, data, nil)
}

// Lookup reports whether path names a loaded template.
func (e *HTMLEngine) Lookup(path string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.tmpl.Lookup(path) != nil
}

// RenderWithFuncs executes the template at path with data, after overlaying funcs
// onto the engine's function map for this render only. funcs is applied with Funcs
// to a Clone() of the parsed template set, never to the shared set, so concurrent
// renders never see each other's functions; this is how the render engine binds a
// per-render "slot" implementation.
//
// html/template resolves a function name to its implementation at execution time,
// but it can only call a name that already existed in the FuncMap the template was
// parsed with. RenderWithFuncs can therefore replace the implementation behind a
// name DefaultFuncs (or HTMLConfig.Funcs) already registered — as it does for
// "slot" — but it cannot make a template call a name that was never registered at
// parse time; that template fails to parse in NewHTML/Reload, before any render is
// attempted, and funcs passed here for such a name are simply never consulted.
//
// If cfg.DevMode is true, RenderWithFuncs reloads the template set from disk before
// rendering. Execution happens into an internal buffer; w only receives output once
// the template has executed successfully, so a failing template never emits a
// partial page.
func (e *HTMLEngine) RenderWithFuncs(ctx context.Context, w io.Writer, path string, data any, funcs template.FuncMap) error { // any: template data
	if err := ctx.Err(); err != nil {
		return err
	}

	if e.cfg.DevMode {
		if err := e.Reload(); err != nil {
			return err
		}
	}

	e.mu.RLock()
	tmpl := e.tmpl
	e.mu.RUnlock()

	if tmpl.Lookup(path) == nil {
		return fmt.Errorf("%w: %s", ErrTemplateNotFound, path)
	}

	clone, err := tmpl.Clone()
	if err != nil {
		return fmt.Errorf("collage: clone template set: %w", err)
	}
	if funcs != nil {
		clone = clone.Funcs(funcs)
	}

	var buf bytes.Buffer
	if err := clone.ExecuteTemplate(&buf, path, data); err != nil {
		return err
	}

	_, err = w.Write(buf.Bytes())
	return err
}

// Reload discards the current template set and reparses every template under
// cfg.Root. It is safe to call concurrently with Render and RenderWithFuncs.
//
// Two overlapping Reload calls (e.g. two concurrent DevMode renders) may finish in
// either order; whichever pointer swap happens last under e.mu wins, even if it was
// the one that started first. This can silently discard a newer parse in favour of
// a stale one already in flight. That's a lost-update race on which parse "wins",
// not a data race — e.mu still makes every read/write of e.tmpl/e.names safe — and
// it's deliberately left unserialized: DevMode reloads are frequent and cheap, and
// the next Render (in DevMode) or the next explicit Reload call corrects it.
//
// Reloading is only meaningful for the disk mode. When cfg.FS is an embed.FS its
// contents are fixed at build time, so a DevMode reload there reparses identical
// bytes on every request: pure cost, no effect.
func (e *HTMLEngine) Reload() error {
	fsys, closeFS, err := e.openRoot()
	if err != nil {
		return err
	}
	defer closeFS()

	funcs := DefaultFuncs()
	maps.Copy(funcs, e.cfg.Funcs)

	set := template.New("").Funcs(funcs)
	var names []string

	walkErr := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if path.Ext(p) != e.cfg.Extension {
			return nil
		}

		// p is the template name as-is: fs.WalkDir yields slash-separated paths
		// relative to the root of fsys, and fsys is already rooted at cfg.Root. No
		// containment check is needed on the path string, because no path string is
		// what containment rests on here — see openRoot.
		content, err := fs.ReadFile(fsys, p)
		if err != nil {
			return e.readError(p, err)
		}
		if _, err := set.New(p).Parse(string(content)); err != nil {
			return fmt.Errorf("collage: parse template %s: %w", p, err)
		}
		names = append(names, p)
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	sort.Strings(names)

	e.mu.Lock()
	e.tmpl = set
	e.names = names
	e.mu.Unlock()
	return nil
}

// openRoot returns the filesystem to load templates from, rooted at cfg.Root, along
// with a function that releases it. Both modes are reduced to one fs.FS here so that
// Reload has a single walk to maintain rather than one per source.
//
// The disk mode goes through os.OpenRoot, which makes containment a kernel
// guarantee: an open that would leave the root is refused by the OS during path
// resolution. That is strictly stronger than resolving symlinks in Go and comparing
// the result against the root as strings, which is what this used to do — that check
// had to be correct about every symlink in every path segment to hold, and had to
// re-derive at every read what the kernel already knows. Note that os.DirFS would
// NOT do: it is explicitly not a security boundary and follows a symlink out of the
// directory without complaint.
//
// A symlink whose target stays inside the root still resolves normally. Containment
// refuses what leaves the root, not symlinks as such.
func (e *HTMLEngine) openRoot() (fs.FS, func(), error) {
	if e.cfg.FS == nil {
		root, err := os.OpenRoot(e.cfg.Root)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %s", ErrTemplateRootMissing, e.cfg.Root)
		}
		return root.FS(), func() { root.Close() }, nil
	}

	// An fs.FS has no symlinks and no absolute paths, so subdirectory selection is
	// all that is left to do: fs.ValidPath already excludes "..", and fs.Sub cannot
	// return a filesystem wider than the one it was given.
	sub := path.Clean(filepath.ToSlash(e.cfg.Root))
	if sub == "" || sub == "." {
		return e.cfg.FS, func() {}, nil
	}
	fsys, err := fs.Sub(e.cfg.FS, sub)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrTemplateRootMissing, e.cfg.Root)
	}
	// fs.Sub does not check that the subdirectory exists, so a typo in Root would
	// otherwise surface as "no templates loaded" rather than as a missing root.
	if info, err := fs.Stat(fsys, "."); err != nil || !info.IsDir() {
		return nil, nil, fmt.Errorf("%w: %s", ErrTemplateRootMissing, e.cfg.Root)
	}
	return fsys, func() {}, nil
}

// readError classifies a failed template read.
//
// os.OpenRoot refuses a symlink whose target leaves the root, but the error it
// returns wraps an unexported value that matches no exported sentinel, so there is
// nothing to test it against without comparing error strings. The escape is
// therefore re-identified here, on the failure path only, to restore the
// ErrTemplateEscapesRoot the caller expects. This lstat carries no security weight:
// the kernel already refused the open, and this only decides which error explains
// the refusal. In FS mode there is nothing to lstat, since an fs.FS has no symlinks.
func (e *HTMLEngine) readError(name string, err error) error {
	if e.cfg.FS == nil {
		info, lerr := os.Lstat(filepath.Join(e.cfg.Root, filepath.FromSlash(name)))
		if lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s: %v", ErrTemplateEscapesRoot, name, err)
		}
	}
	return fmt.Errorf("collage: read template %s: %w", name, err)
}

// Names returns every loaded template path, sorted.
func (e *HTMLEngine) Names() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	names := make([]string, len(e.names))
	copy(names, e.names)
	return names
}

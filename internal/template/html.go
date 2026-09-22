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
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// HTMLConfig configures an HTMLEngine.
type HTMLConfig struct {
	// Root is the directory templates are loaded from.
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
// cfg.Root from disk. It is safe to call concurrently with Render and
// RenderWithFuncs.
//
// Two overlapping Reload calls (e.g. two concurrent DevMode renders) may finish in
// either order; whichever pointer swap happens last under e.mu wins, even if it was
// the one that started first. This can silently discard a newer parse in favour of
// a stale one already in flight. That's a lost-update race on which parse "wins",
// not a data race — e.mu still makes every read/write of e.tmpl/e.names safe — and
// it's deliberately left unserialized: DevMode reloads are frequent and cheap, and
// the next Render (in DevMode) or the next explicit Reload call corrects it.
func (e *HTMLEngine) Reload() error {
	info, err := os.Stat(e.cfg.Root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrTemplateRootMissing, e.cfg.Root)
	}

	// Resolved once per Reload (not per file) and compared against each file's own
	// resolved target below, so a symlinked Root itself (e.g. macOS's /tmp ->
	// /private/tmp) doesn't cause every legitimate template to be rejected.
	rootResolved, err := filepath.EvalSymlinks(e.cfg.Root)
	if err != nil {
		return fmt.Errorf("collage: resolve template root %s: %w", e.cfg.Root, err)
	}

	funcs := DefaultFuncs()
	maps.Copy(funcs, e.cfg.Funcs)

	root := template.New("").Funcs(funcs)
	var names []string

	walkErr := filepath.WalkDir(e.cfg.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(p) != e.cfg.Extension {
			return nil
		}

		name, err := templateName(e.cfg.Root, p)
		if err != nil {
			return err
		}

		// templateName only checks the walked path string, which WalkDir guarantees
		// is lexically under Root. That's not enough: p itself may be a symlink
		// whose target resolves outside Root, and os.ReadFile below follows
		// symlinks transparently. Re-verify against the resolved target before
		// reading it.
		if err := verifyResolvesWithinRoot(rootResolved, p); err != nil {
			return err
		}

		content, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("collage: read template %s: %w", name, err)
		}
		if _, err := root.New(name).Parse(string(content)); err != nil {
			return fmt.Errorf("collage: parse template %s: %w", name, err)
		}
		names = append(names, name)
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	sort.Strings(names)

	e.mu.Lock()
	e.tmpl = root
	e.names = names
	e.mu.Unlock()
	return nil
}

// Names returns every loaded template path, sorted.
func (e *HTMLEngine) Names() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	names := make([]string, len(e.names))
	copy(names, e.names)
	return names
}

// templateName resolves path (a file found under root) to its template name: a
// slash-separated path relative to root, produced with filepath.ToSlash so behaviour
// is identical on macOS and Windows. It returns ErrTemplateEscapesRoot if path does
// not resolve to a location inside root.
func templateName(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrTemplateEscapesRoot, path)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrTemplateEscapesRoot, path)
	}
	return filepath.ToSlash(rel), nil
}

// verifyResolvesWithinRoot follows any symlinks in path and confirms the resolved
// location still falls inside rootResolved, which must already be symlink-resolved
// (see Reload). This closes the gap templateName's lexical check cannot see: path
// can be lexically under Root while being a symlink whose target is not, and
// os.ReadFile follows that symlink transparently. Containment is decided with
// filepath.Rel rather than a string prefix check, so a sibling directory that merely
// shares Root as a string prefix (e.g. "/root" vs "/rootsibling") is not mistaken
// for being inside it.
func verifyResolvesWithinRoot(rootResolved, path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrTemplateEscapesRoot, path, err)
	}
	rel, err := filepath.Rel(rootResolved, resolved)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrTemplateEscapesRoot, path)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s", ErrTemplateEscapesRoot, path)
	}
	return nil
}

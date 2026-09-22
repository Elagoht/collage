package asset

import (
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// ErrInvalidPrefix reports a mount prefix that is empty, "/", or not a
// slash-delimited path. A mount at "/" would swallow every route.
var ErrInvalidPrefix = errors.New("collage: invalid mount prefix")

// ErrNilFS reports that a mount was given no file system.
var ErrNilFS = errors.New("collage: nil mount file system")

// Option configures a Mount.
type Option func(*Mount)

// WithCacheControl sets the Cache-Control header served with every file.
func WithCacheControl(value string) Option {
	return func(m *Mount) { m.cacheControl = value }
}

// WithoutBuildCopy stops a static build from copying this mount into its output.
// Use it for a mount served from a CDN in production, or one large enough that
// duplicating it into the build directory is not wanted.
func WithoutBuildCopy() Option {
	return func(m *Mount) { m.buildCopy = false }
}

// Mount serves an fs.FS under a URL prefix using http.ServeContent, which
// provides Range, If-Range, 206 and Last-Modified handling. Mounted files never
// enter the page cache: their freshness is the client's and the mount's
// Cache-Control's business, not the framework's.
type Mount struct {
	prefix       string
	fsys         fs.FS
	cacheControl string
	buildCopy    bool
	tags         *etagCache
}

// New returns a Mount serving fsys under prefix. prefix must begin and end with
// "/" and must not be "/" alone.
func New(prefix string, fsys fs.FS, opts ...Option) (*Mount, error) {
	if prefix == "" || prefix == "/" || !strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") || strings.HasPrefix(prefix, "//") {
		return nil, ErrInvalidPrefix
	}
	if fsys == nil {
		return nil, ErrNilFS
	}

	m := &Mount{
		prefix:       prefix,
		fsys:         fsys,
		cacheControl: "public, max-age=3600",
		buildCopy:    true,
		tags:         newETagCache(),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m, nil
}

// Prefix returns the URL prefix this mount serves under.
func (m *Mount) Prefix() string { return m.prefix }

// FS returns the file system this mount serves.
func (m *Mount) FS() fs.FS { return m.fsys }

// BuildCopy reports whether a static build should copy this mount into its output.
func (m *Mount) BuildCopy() bool { return m.buildCopy }

// Handles reports whether urlPath falls under this mount's prefix.
func (m *Mount) Handles(urlPath string) bool {
	return strings.HasPrefix(urlPath, m.prefix)
}

// ServeHTTP implements http.Handler.
func (m *Mount) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		plainText(w, r, http.StatusMethodNotAllowed)
		return
	}

	name, ok := m.resolve(r.URL.Path)
	if !ok {
		plainText(w, r, http.StatusNotFound)
		return
	}

	file, err := m.fsys.Open(name)
	if err != nil {
		plainText(w, r, http.StatusNotFound)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		// A directory listing is an information leak; there is no implicit
		// index.html either. Both are deliberate.
		plainText(w, r, http.StatusNotFound)
		return
	}

	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		plainText(w, r, http.StatusInternalServerError)
		return
	}

	if tag, err := m.tags.get(m.fsys, name); err == nil {
		w.Header().Set("ETag", tag)
	}
	if ctype := mime.TypeByExtension(path.Ext(name)); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.Header().Set("Cache-Control", m.cacheControl)

	http.ServeContent(w, r, name, info.ModTime(), seeker)
}

// resolve turns a request path into a path within the mount's file system, or
// reports that it is not servable. Containment is not a string problem: the
// cleaned path must still be a valid fs path, which fs.ValidPath enforces by
// rejecting "..", absolute paths and empty elements.
func (m *Mount) resolve(urlPath string) (string, bool) {
	if !strings.HasPrefix(urlPath, m.prefix) {
		return "", false
	}
	name := path.Clean(strings.TrimPrefix(urlPath, m.prefix))
	if name == "." || name == "/" || strings.HasPrefix(name, "/") {
		return "", false
	}
	if !fs.ValidPath(name) {
		return "", false
	}
	return name, true
}

// plainText writes an error body matching the framework's document error surface:
// never HTML, so a client fetching a stylesheet or a media file is not handed a
// web page.
func plainText(w http.ResponseWriter, r *http.Request, status int) {
	body := http.StatusText(status) + "\n"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write([]byte(body))
	}
}

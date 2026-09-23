// Package minimizer is a collage plugin that strips whitespace and comments from
// what an application serves: rendered pages, documents, and mounted assets.
//
//	app, err := collage.New(&collage.Config{
//		Plugins: []collage.Plugin{minimizer.New()},
//		PluginConfig: pluginConfig, // optional; see Config
//	})
//
// It has to be supplied through Config.Plugins rather than RegisterPlugin, because
// it wraps mounted filesystems and that happens while the application is built.
//
// Everything it does is conservative, and deliberately so — see minify.go. It never
// reorders, renames, rewrites or re-encodes anything. If a saving would require
// understanding the grammar rather than recognising where strings and comments
// begin, it is not taken.
package minimizer

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"path"
	"strings"

	"github.com/Elagoht/collage/pkg/collage"
)

// Name is the plugin's name, and the key its section of the application's plugin
// configuration is found under.
const Name = "elagoht/minimizer"

// Config selects which formats are minified. The zero value minifies nothing, so a
// Config decoded from an absent section leaves the plugin inert rather than
// surprising an application that never asked for it — New's defaults are what turn
// things on.
type Config struct {
	// HTML minifies rendered pages, and any mounted ".html" file.
	HTML bool `json:"html"`
	// JSON minifies documents and mounted files served as JSON. It is exact:
	// encoding/json knows the whole grammar.
	JSON bool `json:"json"`
	// CSS minifies mounted stylesheets.
	CSS bool `json:"css"`
	// JS minifies mounted scripts. Newlines are preserved whatever this is set to;
	// see minifyJS.
	JS bool `json:"js"`
}

// Plugin is the minifier.
type Plugin struct {
	cfg Config
	log *slog.Logger

	// savedIn and savedOut total the bytes seen and emitted, for the summary
	// logged at shutdown. A minifier that is doing nothing is worth noticing, and
	// a percentage is the only form in which anyone notices.
	savedIn  int64
	savedOut int64
}

// New returns a minifier with everything except JavaScript enabled.
//
// JavaScript is off by default because it is the format where the difference
// between a scanner and a parser bites hardest, and because the saving from
// stripping indentation — all this takes, since every newline stays — is the
// smallest of the four. An application that wants it says so.
func New() *Plugin {
	return &Plugin{cfg: Config{HTML: true, JSON: true, CSS: true}}
}

// NewWith returns a minifier configured exactly as given, bypassing the defaults.
func NewWith(cfg Config) *Plugin { return &Plugin{cfg: cfg} }

func (p *Plugin) Name() string    { return Name }
func (p *Plugin) Version() string { return "1.0.0" }

// Configure decodes the application's configuration over whatever New set, and
// registers the filesystem wrapper that minifies mounted assets.
//
// The wrapper is registered unconditionally rather than only when a relevant format
// is enabled, because the configuration decode has already happened by this line
// and a wrapper for a disabled format is a pass-through — one branch per file read,
// against the alternative of a second code path that has to stay in step.
func (p *Plugin) Configure(_ context.Context, host collage.ConfigHost) error {
	p.log = host.Logger()
	if err := host.Config(&p.cfg); err != nil {
		return err
	}
	host.WrapMount(func(inner fs.FS) fs.FS { return &minifyingFS{inner: inner, plugin: p} })
	return nil
}

// Init records the logger for applications that skip Configure — which none can, as
// Configure is what registers the mount wrapper, but a nil logger is not worth
// risking for that reasoning.
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	if p.log == nil {
		p.log = host.Logger()
	}
	return nil
}

// Shutdown logs what the plugin saved.
func (p *Plugin) Shutdown(context.Context) error {
	if p.log == nil || p.savedIn == 0 {
		return nil
	}
	p.log.Info("minimizer: stopped",
		"bytesIn", p.savedIn,
		"bytesOut", p.savedOut,
		"savedPercent", 100-(p.savedOut*100/p.savedIn),
	)
	return nil
}

// OnAfterRender minifies a rendered page.
func (p *Plugin) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	if !p.cfg.HTML {
		return nil
	}
	ev.HTML = p.record(ev.HTML, minifyHTML(ev.HTML))
	return nil
}

// OnDocumentRendered minifies a document whose content type it recognises.
//
// The content type is what decides, not the path: a document declares its type and
// the framework serves exactly that, so it is the one description of the body that
// cannot be wrong.
func (p *Plugin) OnDocumentRendered(_ context.Context, ev *collage.DocumentRenderedEvent) error {
	switch {
	case p.cfg.JSON && isType(ev.ContentType, "json"):
		ev.Body = p.record(ev.Body, minifyJSON(ev.Body))
	case p.cfg.HTML && isType(ev.ContentType, "html"):
		ev.Body = p.record(ev.Body, minifyHTML(ev.Body))
	case p.cfg.CSS && isType(ev.ContentType, "css"):
		ev.Body = p.record(ev.Body, minifyCSS(ev.Body))
	case p.cfg.JS && isType(ev.ContentType, "javascript"):
		ev.Body = p.record(ev.Body, minifyJS(ev.Body))
	}
	return nil
}

// record accounts for a minification and returns whichever of the two to use.
//
// A result larger than the input is discarded. That should not happen, but "should
// not" is not a reason to serve more bytes than arrived, and the check costs a
// comparison.
func (p *Plugin) record(in, out []byte) []byte {
	if len(out) >= len(in) {
		p.savedIn += int64(len(in))
		p.savedOut += int64(len(in))
		return in
	}
	p.savedIn += int64(len(in))
	p.savedOut += int64(len(out))
	return out
}

// isType reports whether a content type names a format, ignoring parameters and
// vendor prefixes: "application/rss+xml" is xml, "text/html; charset=utf-8" is html.
func isType(contentType, format string) bool {
	media, _, _ := strings.Cut(contentType, ";")
	media = strings.TrimSpace(strings.ToLower(media))
	return strings.HasSuffix(media, "/"+format) || strings.HasSuffix(media, "+"+format)
}

// minifyingFS minifies a mounted filesystem's files as they are read.
//
// It wraps the filesystem rather than the response because mounts serve through
// http.ServeContent, which brings Range, If-Range and 206 with it. Minifying per
// request would shift every byte offset, so a range request would return the wrong
// slice of a file whose advertised length no longer matched. Minifying here means
// the file *is* the minified one, and ServeContent's arithmetic stays true.
type minifyingFS struct {
	inner  fs.FS
	plugin *Plugin
}

func (m *minifyingFS) Open(name string) (fs.File, error) {
	f, err := m.inner.Open(name)
	if err != nil {
		return nil, err
	}

	minify := m.minifierFor(name)
	if minify == nil {
		return f, nil
	}

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return f, err
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(f); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()

	return &minifiedFile{
		Reader: bytes.NewReader(m.plugin.record(buf.Bytes(), minify(buf.Bytes()))),
		info:   info,
	}, nil
}

func (m *minifyingFS) minifierFor(name string) func([]byte) []byte {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm":
		if m.plugin.cfg.HTML {
			return minifyHTML
		}
	case ".json":
		if m.plugin.cfg.JSON {
			return minifyJSON
		}
	case ".css":
		if m.plugin.cfg.CSS {
			return minifyCSS
		}
	case ".js", ".mjs":
		if m.plugin.cfg.JS {
			return minifyJS
		}
	}
	return nil
}

// minifiedFile is the minified content presented as an fs.File.
//
// It is a *bytes.Reader, and that is what makes ranges work: the mount hands the
// file to http.ServeContent as an io.ReadSeeker, and ServeContent takes the length
// by seeking to the end rather than from Stat. So the length it advertises is the
// minified length by construction, and every offset it computes is an offset into
// the bytes actually served.
//
// Stat still reports the minified size, but that is correctness for other readers —
// a static build copying the mount, anything calling fs.Stat — rather than what the
// HTTP path depends on. ModTime is kept from the original, so If-Modified-Since
// still behaves.
type minifiedFile struct {
	*bytes.Reader
	info fs.FileInfo
}

func (f *minifiedFile) Stat() (fs.FileInfo, error) {
	return &minifiedInfo{FileInfo: f.info, size: int64(f.Reader.Size())}, nil
}

func (f *minifiedFile) Close() error { return nil }

type minifiedInfo struct {
	fs.FileInfo
	size int64
}

func (i *minifiedInfo) Size() int64 { return i.size }

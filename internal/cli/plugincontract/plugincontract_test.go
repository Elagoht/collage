package plugincontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseRef(t *testing.T) {
	for _, tc := range []struct {
		in             string
		path, short, v string
		bad            bool
	}{
		{in: "elagoht/consent", path: "github.com/elagoht/collage-consent", short: "elagoht/consent"},
		{in: "Elagoht/Consent", path: "github.com/elagoht/collage-consent", short: "elagoht/consent"},
		{in: "elagoht/consent@v0.1.2", path: "github.com/elagoht/collage-consent", short: "elagoht/consent", v: "v0.1.2"},
		{in: "github.com/kisi/repo", path: "github.com/kisi/repo"},
		{in: "gitlab.com/x/y/z@latest", path: "gitlab.com/x/y/z", v: "latest"},
		{in: "consent", bad: true},
		{in: "a/b/c", bad: true}, // three segments without a dot: neither form
		{in: "elagoht/", bad: true},
		{in: "elagoht/con sent", bad: true},
		{in: "elagoht/consent@", bad: true},
		{in: "", bad: true},
	} {
		r, err := ParseRef(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("%q: no error", tc.in)
			}
			continue
		}
		if err != nil || r.Path != tc.path || r.Short != tc.short || r.Version != tc.v {
			t.Errorf("%q = %+v, %v; want %s %q %q", tc.in, r, err, tc.path, tc.short, tc.v)
		}
	}
}

func TestIsPseudoVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"v0.1.2": false, "v1.0.0-rc.1": false,
		"v0.0.0-20260101000000-abcdefabcdef":              true,
		"v0.1.3-0.20260101000000-abcdefabcdef":            true,
		"v1.2.4-pre.0.20260101000000-abcdefabcdef":        true,
		"v2.0.0-20260101000000-abcdefabcdef+incompatible": true,
	} {
		if IsPseudoVersion(v) != want {
			t.Errorf("%s: %v", v, !want)
		}
	}
}

const pluginSrc = `package %s

import "context"

type Options struct{ A string }
type Store interface{ Put() }
type Mode int

%s

type Plugin struct{}

func (*Plugin) Name() string { return "x/y" }
func (*Plugin) Version() string { return "0" }
func (*Plugin) Shutdown(context.Context) error { return nil }
`

func TestReadConstructor(t *testing.T) {
	for _, tc := range []struct {
		decl, call string
		bad        string // substring of the error
	}{
		{decl: "func New() *Plugin { return nil }", call: "p.New()"},
		{decl: "func New(feeds ...string) *Plugin { return nil }", call: "p.New()"},
		{decl: "func New(opts Options) *Plugin { return nil }", call: "p.New(p.Options{})"},
		{decl: "func New(s Store) *Plugin { return nil }", call: "p.New(nil)"},
		{decl: "func New(o *Options) *Plugin { return nil }", call: "p.New(nil)"},
		{decl: "func New(f func()) *Plugin { return nil }", call: "p.New(nil)"},
		{decl: "func New(m map[string]int) *Plugin { return nil }", call: "p.New(nil)"},
		{decl: "func New(xs []int) *Plugin { return nil }", call: "p.New(nil)"},
		{decl: "func New(c context.Context) *Plugin { return nil }", call: "p.New(nil)"}, // an interface from another package
		{decl: "func New(a, b string) *Plugin { return nil }", bad: "two parameters"},
		{decl: "func New(m Mode) *Plugin { return nil }", bad: "Mode"},
		{decl: "func New(s string) *Plugin { return nil }", bad: "string"},
		{decl: "func New() (*Plugin, error) { return nil, nil }", bad: "one result"},
		{decl: "func (p *Plugin) New() *Plugin { return nil }", bad: "no func New"},
		{decl: "", bad: "no func New"},
	} {
		dir := t.TempDir()
		write(t, dir, map[string]string{"p.go": fmtSrc("p", tc.decl), "p_test.go": "package p\n"})
		c, err := ReadConstructor(dir)
		if tc.bad != "" {
			if err == nil || !strings.Contains(err.Error(), tc.bad) {
				t.Errorf("%q: err = %v, want %q", tc.decl, err, tc.bad)
			}
			continue
		}
		if err != nil || c.Call("p") != tc.call {
			t.Errorf("%q: %q, %v; want %q", tc.decl, c.Call("p"), err, tc.call)
		}
	}
}

func fmtSrc(pkg, decl string) string {
	return strings.Replace(strings.Replace(pluginSrc, "%s", pkg, 1), "%s", decl, 1)
}

func TestReadManifest(t *testing.T) {
	good := `{"$schema":"x","name":"elagoht/consent","description":"d","repository":"https://x","config":{},
	  "attributes":[],"templateFunctions":[],"snippets":{},"requires":["elagoht/live"],"order":-30,"setup":"note"}`
	dir := t.TempDir()
	write(t, dir, map[string]string{"collage.json": good})
	m, err := ReadManifest(dir)
	if err != nil || m.Name != "elagoht/consent" || m.Order != -30 || m.Setup != "note" || len(m.Requires) != 1 {
		t.Fatalf("%+v, %v", m, err)
	}
	for name, body := range map[string]string{
		"unknown key": `{"name":"a/b","extra":1}`,
		"bad name":    `{"name":"A/b"}`,
		"no name":     `{}`,
		"bad require": `{"name":"a/b","requires":["nope"]}`,
		"order type":  `{"name":"a/b","order":"last"}`,
		"not json":    `{`,
	} {
		d := t.TempDir()
		write(t, d, map[string]string{"collage.json": body})
		if _, err := ReadManifest(d); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := ReadManifest(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no collage.json") {
		t.Errorf("missing manifest: %v", err)
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"collage.json": `{"name":"elagoht/consent"}`,
		"p.go":         fmtSrc("consent", "func New() *Plugin { return nil }"),
	})
	p, err := Check(dir, "elagoht/consent")
	if err != nil || p.Constructor.Call("consent") != "consent.New()" {
		t.Fatalf("%+v, %v", p, err)
	}
	if _, err := Check(dir, "elagoht/other"); err == nil || !strings.Contains(err.Error(), `"elagoht/consent"`) {
		t.Errorf("short ref mismatch: %v", err)
	}
	if _, err := Check(dir, ""); err != nil {
		t.Errorf("full ref skips the name match: %v", err)
	}
	if _, err := Check(t.TempDir(), ""); err == nil || !strings.Contains(err.Error(), AuthorsURL) {
		t.Errorf("errors point at the authors page: %v", err)
	}
}

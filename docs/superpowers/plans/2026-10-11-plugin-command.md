# `collage plugin` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `collage plugin add|del|check`. A plugin is added to or removed from a
project in one command: `go get`, an entry in a CLI-managed `plugins.go`, and an
empty section in `plugins-config.json`. Any repo that meets a checkable contract
can be added.

**Architecture:**
- A new package `internal/cli/plugincontract` reads and checks a downloaded
  module. It covers the manifest, the `New` signature and the version.
- Two pure editors live in `internal/cli`: one for `plugins.go` (go/ast) and one
  for `plugins-config.json` (textual).
- The `plugin` command glues them to the go tool through the CLI's existing
  `CommandRunner`, with snapshot and restore of the four files it may write.
- Tests run offline against a fake `GOPROXY=file://` module proxy.
- Core gains one additive helper, `collage.FindPlugin`. The plugin repos get
  small follow-ups.

**Tech Stack:** Go 1.26, stdlib only (go/parser, go/ast, go/format,
encoding/json, archive/zip). No golang.org/x modules.

**Spec:** `docs/superpowers/specs/2026-10-11-plugin-command-design.md`

## Global Constraints

- Never use the Go type `any` in new code. The only exception is restating a
  stdlib signature, with the existing `// any: …` comment style.
- Stdlib only. Do not add `golang.org/x/mod` or any other dependency to collage's
  go.mod.
- Short ref: `owner/name`, two segments, the first without a dot. It resolves to
  `github.com/<owner>/collage-<name>`. A full ref is a module path whose first
  segment contains a dot, and is used as written.
- Manifest name: `^[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9-]*$`.
- New manifest fields: `requires` (array of names), `order` (integer, default 0,
  lower is earlier), `setup` (Markdown string).
- Ours: health −30, fail2ban −25, compress −20, devtoolbar 100, websocket −1.
- Config section added: `"<name>": {}`. An existing section is never touched by
  `add`.
- `add` and `del` are all-or-nothing: `go.mod`, `go.sum`, `plugins.go` and
  `plugins-config.json` are byte-identical to before on any failure. A file that
  was absent stays absent.
- Tests never reach the network or the user's module cache. Every test that runs
  the go tool sets these env vars, through the helper in Task 4:
  - `GOPROXY=file://…`, `GOSUMDB=off`, `GONOSUMDB=*`;
  - `GOFLAGS=-mod=mod -modcacherw`, `GOMODCACHE=<tempdir>`;
  - `GOTOOLCHAIN=local`, `GOWORK=off`.
- Every test that runs the go tool starts with
  `if _, err := exec.LookPath("go"); err != nil { t.Skip("go binary not available") }`,
  as the existing CLI tests do.
- Run tests with `go test -count=1` (memory: the test cache hides scaffold
  breakage).
- Commit trailer: `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- Never `git add -A`. Add files by name after reading `git status`.
- Work in a git worktree off `main` (another session may use `~/Desktop/collage`).
- Errors from the plugin command start `collage: plugin <sub>: `.

## Review Focus

1. **A `plugins.go` the user edited** (comments, a `sitemap.New(sitemap.Options{BaseURL: …})`
   entry, a hand-reordered list, an extra statement before `return`).
   `add` and `del` change only the one entry and its import; the rest is
   kept, apart from gofmt. Test in Task 3.
2. **`plugins-config.json` in other formats:** tabs, CRLF line endings, no
   trailing newline, a `$schema` member, `{}`, a single-line object. `add`
   and `del` keep every byte outside the edited member. Test in Task 3.
3. **A failure after `go get` succeeded:** the contract fails, or `go mod tidy`
   fails. All four files are restored byte for byte, and files that did not
   exist are removed again. Test in Task 4 (contract) and Task 5 (tidy).
4. **Mixed case.** The user types `Elagoht/Consent`. The module is served at
   `github.com/elagoht/…` but declares `github.com/Elagoht/…`. `add`
   succeeds, and `del Elagoht/Consent` and `del elagoht/consent` both find
   it. Test in Task 5 and Task 6.
5. **Import name clashes:** two plugins whose packages share a name, or a
   plugin whose package name equals an import already in `plugins.go` (such
   as `collage`). Test in Task 3.

## Spec amendments made while planning

These are already applied to the spec file in this plan's first commit.

- **§2 rule 1.** "Validates against the manifest schema" becomes "decodes
  strictly". The CLI cannot run JSON Schema with stdlib alone. It decodes the
  manifest with `DisallowUnknownFields` into a struct that lists every
  top-level key the schema allows, and checks the fields it uses. The editor
  extension keeps the full schema.
- **§7 websocket and §8.** `FindPlugin` returns any plugin in the application's
  list, whatever its init state, from both `Configure` and `Init`. live
  requires `UseTransport` before the application is built, so websocket finds
  live in `Configure` and must be configured first. websocket's `order` is
  therefore −1, not 1.
- **§8.** The `collage check` addition is dropped. A `plugins.go` entry whose
  module is gone fails `go build`, and `collage check` runs the program, so the
  check would add nothing.
- **§3.** "the package `go list` reports as main" becomes "the directory given
  by `-dir` (default `.`), which must hold a non-test `package main` file". No
  `go list` call is needed.

---

## File Structure

| File | Responsibility |
|---|---|
| `pkg/collage/findplugin.go` (create) | `FindPlugin[T Plugin](host ConfigReader) (T, bool)` |
| `internal/plugin/plugin.go` (modify) | `Lister` interface |
| `internal/core/host.go` (modify) | `ApplicationPlugins()` on `hostView` and `configHostView` |
| `internal/cli/plugincontract/manifest.go` (create) | `Manifest`, `ReadManifest`, `ValidName` |
| `internal/cli/plugincontract/ref.go` (create) | `Ref`, `ParseRef`, `IsPseudoVersion` |
| `internal/cli/plugincontract/constructor.go` (create) | `Constructor`, `ReadConstructor` |
| `internal/cli/plugincontract/check.go` (create) | `Check(dir, short)`: rules 1–4 |
| `internal/cli/pluginsgo.go` (create) | load/edit/format `plugins.go` |
| `internal/cli/pluginconfig.go` (create) | textual add/remove of a member in `plugins-config.json` |
| `internal/cli/gotool.go` (create) | captured go commands, `go list -m -json`, file snapshots |
| `internal/cli/plugin.go` (create) | the `plugin` command: dispatch, `add`, `del` |
| `internal/cli/plugincheck.go` (create) | `plugin check`, `-release`, `-run` |
| `internal/cli/fakeproxy_test.go` (create) | the fake module proxy and fixture plugins |
| `internal/cli/cli.go` (modify) | builtins row, dispatch, help |
| `internal/cli/scaffold/common/plugins.go.tmpl` (create), `main.go.tmpl` (modify) | scaffold |
| `CHANGELOG.md` (modify) | v0.58.0 |

---

### Task 1: `collage.FindPlugin`

**Files:**
- Modify: `internal/plugin/plugin.go` (add the `Lister` interface after `ConfigReader`)
- Modify: `internal/core/host.go` (methods on `hostView` and `configHostView`)
- Create: `pkg/collage/findplugin.go`
- Test: `pkg/collage/findplugin_test.go`

**Interfaces:**
- Produces: `func FindPlugin[T Plugin](host ConfigReader) (T, bool)` in `pkg/collage`.
  Task 9 (websocket) uses `collage.FindPlugin[*live.Plugin](host)` from `Configure`.
- Produces: `plugin.Lister interface { ApplicationPlugins() []Plugin }`.

- [ ] **Step 1: Write the failing test**

```go
package collage_test

import (
	"context"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

type carrier struct{ name string }

func (c *carrier) Name() string                             { return c.name }
func (c *carrier) Version() string                          { return "0" }
func (c *carrier) Init(context.Context, collage.Host) error { return nil }
func (c *carrier) Shutdown(context.Context) error           { return nil }

// seeker looks for a *carrier in Configure and in Init, and keeps what it found.
type seeker struct {
	inConfigure, inInit *carrier
}

func (s *seeker) Name() string    { return "test/seeker" }
func (s *seeker) Version() string { return "0" }
func (s *seeker) Configure(_ context.Context, host collage.ConfigHost) error {
	s.inConfigure, _ = collage.FindPlugin[*carrier](host)
	return nil
}
func (s *seeker) Init(_ context.Context, host collage.Host) error {
	s.inInit, _ = collage.FindPlugin[*carrier](host)
	return nil
}
func (s *seeker) Shutdown(context.Context) error { return nil }

// A plugin finds another by type, in Configure and in Init, wherever the other
// sits in the list: listed after it, it is found before it was configured.
func TestFindPlugin(t *testing.T) {
	c := &carrier{name: "test/carrier"}
	s := &seeker{}
	app, err := collage.New(&collage.Config{Plugins: []collage.Plugin{s, c}})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil { // Init runs when the application starts
		t.Fatal(err)
	}
	if s.inConfigure != c || s.inInit != c {
		t.Fatalf("Configure found %p, Init found %p, want %p", s.inConfigure, s.inInit, c)
	}
}

// No plugin of the type: the zero value and false.
func TestFindPlugin_None(t *testing.T) {
	s := &seeker{}
	app, err := collage.New(&collage.Config{Plugins: []collage.Plugin{s}})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	if s.inConfigure != nil || s.inInit != nil {
		t.Fatal("found a carrier that is not there")
	}
}

// A host collage did not make (a test double) has no plugins to offer.
// plugin.ConfigSource satisfies ConfigReader on its own, as a test double does.
func TestFindPlugin_ForeignHost(t *testing.T) {
	if _, ok := collage.FindPlugin[*carrier](plugin.ConfigSource{Name: "x/y"}); ok {
		t.Fatal("found a plugin through a host with none")
	}
}
```

Add `"github.com/Elagoht/collage/internal/plugin"` to the test's imports. The
test lives inside the module, so the internal import is allowed.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 -run FindPlugin ./pkg/collage/`
Expected: FAIL, build error `undefined: collage.FindPlugin`

- [ ] **Step 3: Write minimal implementation**

`internal/plugin/plugin.go`, after the `ConfigReader` interface:

```go
// Lister is implemented by the hosts collage hands plugins: the application's
// plugins, in the order they are listed, whether or not each has been
// configured or initialised yet. collage.FindPlugin reads it.
type Lister interface {
	ApplicationPlugins() []Plugin
}
```

`internal/core/host.go`, one method on each view:

```go
// ApplicationPlugins is plugin.Lister: every plugin the application has, in
// order. collage.FindPlugin reads it.
func (h *hostView) ApplicationPlugins() []plugin.Plugin { return h.app.plugins.Plugins() }

// ApplicationPlugins is plugin.Lister, as on hostView: a plugin looking for
// another in Configure finds it whether it is listed before or after.
func (h *configHostView) ApplicationPlugins() []plugin.Plugin { return h.app.plugins.Plugins() }
```

`pkg/collage/findplugin.go`:

```go
package collage

import "github.com/Elagoht/collage/internal/plugin"

// FindPlugin returns the application's plugin of type T, and whether there is
// one: the first in the list, when several are. host is the ConfigHost given to
// Configure or the Host given to Init. The plugin is found wherever it is
// listed, and may not be configured or initialised yet. A plugin that needs
// another to be set up first calls it from Configure, which runs before any
// plugin is built.
//
//	lv, ok := collage.FindPlugin[*live.Plugin](host)
//
// A host collage did not make (a test double) has no plugins, and FindPlugin
// reports false.
func FindPlugin[T Plugin](host ConfigReader) (T, bool) {
	var zero T
	lister, ok := host.(plugin.Lister)
	if !ok {
		return zero, false
	}
	for _, p := range lister.ApplicationPlugins() {
		if t, ok := p.(T); ok {
			return t, true
		}
	}
	return zero, false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -count=1 -run FindPlugin ./pkg/collage/ && go vet ./pkg/collage/ ./internal/core/ ./internal/plugin/`
Expected: PASS, no vet output

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/plugin.go internal/core/host.go pkg/collage/findplugin.go pkg/collage/findplugin_test.go
git commit -m "feat: collage.FindPlugin finds another plugin by type, from Configure or Init"
```

---

### Task 2: `internal/cli/plugincontract`

**Files:**
- Create: `internal/cli/plugincontract/manifest.go`, `ref.go`, `constructor.go`, `check.go`
- Test: `internal/cli/plugincontract/plugincontract_test.go`

**Interfaces:**
- Produces:

```go
type Manifest struct {
	Name     string   `json:"name"`
	Requires []string `json:"requires"`
	Order    int      `json:"order"`
	Setup    string   `json:"setup"`
}
func ReadManifest(dir string) (Manifest, error)
func ValidName(name string) bool

type Ref struct {
	Path    string // module path to go get
	Short   string // "owner/name", lowercased, for a short ref; "" for a full one
	Version string // after "@", "" when none was given
}
func ParseRef(s string) (Ref, error)
func ConventionalPath(short string) string // "elagoht/consent" → "github.com/elagoht/collage-consent"
func IsPseudoVersion(v string) bool

type Constructor struct {
	Package string // the package name its source declares
	Arg     string // "" (no argument), "nil", or a type name declared in the package, e.g. "Options"
}
func (c Constructor) Call(ident string) string // "consent.New()", "sitemap.New(sitemap.Options{})", "uploads.New(nil)"
func ReadConstructor(dir string) (Constructor, error)

type Plugin struct {
	Manifest    Manifest
	Constructor Constructor
}
func Check(dir, short string) (Plugin, error) // rules 1–4; short is Ref.Short ("" skips the match)
const AuthorsURL = "https://collage.furkanbaytekin.dev/en/docs/plugin-authors/"
```

- [ ] **Step 1: Write the failing tests**

```go
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
		{in: "a/b/c", bad: true},          // three segments without a dot: neither form
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
		"v0.0.0-20260101000000-abcdefabcdef":         true,
		"v0.1.3-0.20260101000000-abcdefabcdef":       true,
		"v1.2.4-pre.0.20260101000000-abcdefabcdef":   true,
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

func fmtSrc(pkg, decl string) string { return strings.Replace(strings.Replace(pluginSrc, "%s", pkg, 1), "%s", decl, 1) }

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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/cli/plugincontract/`
Expected: FAIL, build errors (undefined `ParseRef`, `ReadConstructor`, …)

- [ ] **Step 3: Write the implementation**

`ref.go`:

```go
// Package plugincontract is the rule a module meets to be a collage plugin
// "collage plugin add" accepts: a manifest at its root, a well-formed name, a
// New it can call without configuration, a release tag. It reads a module on
// disk and says which rule it breaks.
package plugincontract

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// AuthorsURL is the page that states the contract; every refusal points at it.
const AuthorsURL = "https://collage.furkanbaytekin.dev/en/docs/plugin-authors/"

// Ref is a plugin reference as typed after "collage plugin add" or "del".
type Ref struct {
	// Path is the module path to fetch.
	Path string
	// Short is "owner/name", lowercased, when the reference was short; "" for a
	// full module path.
	Short string
	// Version is what followed "@", or "" when nothing did.
	Version string
}

var segment = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ParseRef reads s: "owner/name" (resolved by ConventionalPath) or a module
// path whose first segment has a dot, either one optionally followed by
// "@version".
func ParseRef(s string) (Ref, error) {
	path, version, hasAt := strings.Cut(s, "@")
	if hasAt && version == "" {
		return Ref{}, fmt.Errorf("%q: an empty version after @", s)
	}
	parts := strings.Split(path, "/")
	if len(parts) > 0 && strings.Contains(parts[0], ".") {
		for _, p := range parts {
			if p == "" || strings.ContainsAny(p, " \t\\") {
				return Ref{}, fmt.Errorf("%q is not a module path", path)
			}
		}
		if len(parts) < 2 {
			return Ref{}, fmt.Errorf("%q is not a module path", path)
		}
		return Ref{Path: path, Version: version}, nil
	}
	if len(parts) != 2 {
		return Ref{}, fmt.Errorf("%q: want owner/name, or a module path such as github.com/owner/repo", s)
	}
	short := strings.ToLower(path)
	owner, name, _ := strings.Cut(short, "/")
	if !segment.MatchString(owner) || !segment.MatchString(name) {
		return Ref{}, fmt.Errorf("%q: owner and name are letters, digits and dashes", s)
	}
	return Ref{Path: ConventionalPath(short), Short: short, Version: version}, nil
}

// ConventionalPath is where a short name's module lives:
// "elagoht/consent" is github.com/elagoht/collage-consent.
func ConventionalPath(short string) string {
	owner, name, _ := strings.Cut(short, "/")
	return "github.com/" + owner + "/collage-" + name
}

var pseudo = regexp.MustCompile(`(^|[-.])\d{14}-[0-9a-f]{12}(\+incompatible)?$`)

// IsPseudoVersion reports whether v is a pseudo-version: a commit, not a
// release.
func IsPseudoVersion(v string) bool { return pseudo.MatchString(v) }

// errContract wraps every rule refusal, so a caller can tell a refusal from an
// I/O failure.
var errContract = errors.New("not a collage plugin")
```

`manifest.go`:

```go
package plugincontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// Manifest is the part of a plugin's collage.json the CLI uses.
type Manifest struct {
	Name     string   `json:"name"`
	Requires []string `json:"requires"`
	Order    int      `json:"order"`
	Setup    string   `json:"setup"`
}

// manifestFile lists every top-level key the manifest schema allows, so an
// unknown one is refused as the schema's additionalProperties: false would.
type manifestFile struct {
	Schema            json.RawMessage `json:"$schema"`
	Name              string          `json:"name"`
	Description       json.RawMessage `json:"description"`
	Repository        json.RawMessage `json:"repository"`
	TemplateFunctions json.RawMessage `json:"templateFunctions"`
	Attributes        json.RawMessage `json:"attributes"`
	Snippets          json.RawMessage `json:"snippets"`
	Config            json.RawMessage `json:"config"`
	Requires          []string        `json:"requires"`
	Order             int             `json:"order"`
	Setup             string          `json:"setup"`
}

var name = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9-]*$`)

// ValidName reports whether s is a plugin name: "owner/name", lowercase.
func ValidName(s string) bool { return name.MatchString(s) }

// ReadManifest reads and checks dir/collage.json.
func ReadManifest(dir string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "collage.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, refuse("no collage.json at the module root")
	}
	if err != nil {
		return Manifest{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f manifestFile
	if err := dec.Decode(&f); err != nil {
		return Manifest{}, refuse(fmt.Sprintf("collage.json: %v", err))
	}
	if !ValidName(f.Name) {
		return Manifest{}, refuse(fmt.Sprintf("collage.json: name %q is not owner/name in lowercase letters, digits and dashes", f.Name))
	}
	for _, r := range f.Requires {
		if !ValidName(r) {
			return Manifest{}, refuse(fmt.Sprintf("collage.json: requires %q, which is not a plugin name", r))
		}
	}
	return Manifest{Name: f.Name, Requires: f.Requires, Order: f.Order, Setup: f.Setup}, nil
}
```

`check.go`:

```go
package plugincontract

import "fmt"

// Plugin is what Check learned about a module that meets the contract.
type Plugin struct {
	Manifest    Manifest
	Constructor Constructor
}

// refuse is a contract refusal: the reason, and where the contract is written.
// Callers format the reason with fmt.Sprintf first, so no variadic parameter of
// our own takes arbitrary values.
func refuse(reason string) error {
	return fmt.Errorf("%w: %s; see %s", errContract, reason, AuthorsURL)
}

// Check checks rules 1–4 on the module at dir. short is the reference's
// Ref.Short; when it is not "", the manifest's name must equal it.
func Check(dir, short string) (Plugin, error) {
	m, err := ReadManifest(dir)
	if err != nil {
		return Plugin{}, err
	}
	if short != "" && m.Name != short {
		return Plugin{}, refuse(fmt.Sprintf("collage.json names the plugin %q, not %q", m.Name, short))
	}
	c, err := ReadConstructor(dir)
	if err != nil {
		return Plugin{}, err
	}
	return Plugin{Manifest: m, Constructor: c}, nil
}
```

Calling `fmt.Sprintf`, whose own signature has `...any`, is calling a stdlib
signature, which the constraint allows. Declaring a parameter of that type
ourselves is not allowed.

`constructor.go`:

```go
package plugincontract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Constructor is how "collage plugin add" calls a plugin's New.
type Constructor struct {
	// Package is the package name the plugin's source declares.
	Package string
	// Arg is what New is passed: "" for nothing, "nil", or the name of a struct
	// type the package declares, passed as its zero value.
	Arg string
}

// Call is the call written into plugins.go, with ident naming the import.
func (c Constructor) Call(ident string) string {
	switch c.Arg {
	case "":
		return ident + ".New()"
	case "nil":
		return ident + ".New(nil)"
	}
	return ident + ".New(" + ident + "." + c.Arg + "{})"
}

// ReadConstructor finds the package-level func New in dir's non-test Go files
// and works out how to call it with nothing configured.
func ReadConstructor(dir string) (Constructor, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Constructor{}, err
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			return Constructor{}, refuse(fmt.Sprintf("%s does not parse: %v", n, err))
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return Constructor{}, refuse("no Go package at the module root")
	}
	pkg := files[0].Name.Name
	types := map[string]ast.Expr{} // type name → its definition
	var fn *ast.FuncDecl
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.Name == "New" && fn == nil {
					fn = d
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok {
						types[ts.Name.Name] = ts.Type
					}
				}
			}
		}
	}
	if fn == nil {
		return Constructor{}, refuse("no func New at the module root")
	}
	if fn.Type.Results == nil || fieldCount(fn.Type.Results) != 1 {
		return Constructor{}, refuse("New must return exactly one result, the plugin")
	}
	params := fn.Type.Params
	switch n := fieldCount(params); {
	case n == 0:
		return Constructor{Package: pkg}, nil
	case n == 1:
		t := params.List[0].Type
		if _, ok := t.(*ast.Ellipsis); ok {
			return Constructor{Package: pkg}, nil
		}
		arg, err := zeroArg(t, types)
		if err != nil {
			return Constructor{}, err
		}
		return Constructor{Package: pkg, Arg: arg}, nil
	default:
		return Constructor{}, refuse(fmt.Sprintf("New takes %d parameters (two parameters or more cannot be filled in); it may take none, or one whose zero value works", n))
	}
}

// fieldCount counts the names in a parameter or result list: "a, b string" is two.
func fieldCount(l *ast.FieldList) int {
	n := 0
	for _, f := range l.List {
		if len(f.Names) == 0 {
			n++
		} else {
			n += len(f.Names)
		}
	}
	return n
}

// zeroArg is how the zero value of t is written in plugins.go.
func zeroArg(t ast.Expr, types map[string]ast.Expr) (string, error) {
	switch t := t.(type) {
	case *ast.StarExpr, *ast.ArrayType, *ast.MapType, *ast.FuncType, *ast.ChanType, *ast.InterfaceType:
		if a, ok := t.(*ast.ArrayType); ok && a.Len != nil {
			return "", refuse("New's parameter is a fixed-size array; it may take none, or one whose zero value works")
		}
		return "nil", nil
	case *ast.SelectorExpr:
		// A type from another package: only an interface can be written
		// without importing that package, and only nil says it. A struct from
		// elsewhere is refused rather than guessed at.
		return "nil", nil
	case *ast.Ident:
		def, ok := types[t.Name]
		if !ok {
			return "", refuse(fmt.Sprintf("New takes a %s; it may take none, or one parameter whose zero value works (a struct, pointer, interface, slice, map or func)", t.Name))
		}
		switch def.(type) {
		case *ast.StructType:
			return t.Name, nil
		case *ast.InterfaceType:
			return "nil", nil
		}
		return "", refuse(fmt.Sprintf("New takes a %s, whose zero value cannot be written for it; use a struct of options", t.Name))
	}
	return "", refuse("New's parameter has a type collage plugin add cannot fill in")
}
```

The `*ast.SelectorExpr` case returns `nil` for any type from another package.
`context.Context` is an interface, so `nil` compiles. A struct from another
package would not compile with `nil`. A plugin with that shape fails `go build`
in Task 4's flow, and `add` reports it, which is acceptable because no plugin of
ours has that shape. Note this in the Task 2 report.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/cli/plugincontract/ && go vet ./internal/cli/plugincontract/`
Expected: PASS

- [ ] **Step 5: Check for `any` and empty interfaces**

Run: `grep -nE '\bany\b|interface\{ ?\}' internal/cli/plugincontract/*.go`
Expected: no output

- [ ] **Step 6: Commit**

```bash
git add internal/cli/plugincontract/
git commit -m "feat(cli): plugincontract reads a module's manifest and New, and refuses what is not a plugin"
```

---

### Task 3: the `plugins.go` and `plugins-config.json` editors

**Files:**
- Create: `internal/cli/pluginsgo.go`, `internal/cli/pluginconfig.go`
- Test: `internal/cli/pluginsgo_test.go`, `internal/cli/pluginconfig_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks. These are pure functions over bytes.
- Produces:

```go
// pluginsgo.go
type pluginsFile struct{ /* unexported */ }
type pluginEntry struct {
	Import string // import path
	Ident  string // the name the call uses
}
func parsePluginsFile(src []byte) (*pluginsFile, error)   // src nil → the empty skeleton
func (f *pluginsFile) entries() []pluginEntry               // in list order; unattributable entries are skipped
func (f *pluginsFile) has(importPath string) bool           // case-insensitive
func (f *pluginsFile) insert(importPath, pkgName string, call func(ident string) string, at int) string // returns ident
func (f *pluginsFile) remove(importPath string) bool        // case-insensitive
func (f *pluginsFile) format() ([]byte, error)
func insertIndex(orders []int, order int) int               // first index whose order > order, else len

// pluginconfig.go
func addConfigSection(src []byte, name string) (out []byte, added bool, err error)
func removeConfigSection(src []byte, name string) (out []byte, removed []byte, err error) // removed is the member's raw value, nil when absent
```

`insert`'s `at` is an index into `entries()`. The new element goes into the
composite literal immediately before the element of `entries()[at]`, or at the
end when `at == len(entries())`. Unattributable elements stay where they are.
Task 4 computes `at` with `insertIndex` from the existing entries' manifest
orders.

- [ ] **Step 1: Write the failing tests for `plugins.go`**

```go
package cli

import (
	"strings"
	"testing"
)

const edited = `// Code generated by collage plugin, and yours to edit.

package main

import (
	"github.com/Elagoht/collage/pkg/collage"

	sitemap "github.com/Elagoht/collage-sitemap"
	devtoolbar "github.com/Elagoht/collage-devtoolbar"
)

// plugins are the application's plugins.
func plugins() []collage.Plugin {
	base := "https://example.com" // a statement of the user's own
	return []collage.Plugin{
		// the sitemap needs the site's address
		sitemap.New(sitemap.Options{BaseURL: base}),
		devtoolbar.New(),
	}
}
`

func TestPluginsFile_Skeleton(t *testing.T) {
	f, err := parsePluginsFile(nil)
	if err != nil {
		t.Fatal(err)
	}
	ident := f.insert("github.com/Elagoht/collage-consent", "consent", func(id string) string { return id + ".New()" }, 0)
	out, err := f.format()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if ident != "consent" || !strings.Contains(s, `consent "github.com/Elagoht/collage-consent"`) ||
		!strings.Contains(s, "consent.New(),") || !strings.Contains(s, "func plugins() []collage.Plugin") {
		t.Fatalf("%s", s)
	}
}

// Review Focus 1: only the one entry and its import change.
func TestPluginsFile_KeepsTheUsersEdits(t *testing.T) {
	f, err := parsePluginsFile([]byte(edited))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.entries(); len(got) != 2 || got[0].Ident != "sitemap" || got[1].Ident != "devtoolbar" {
		t.Fatalf("entries %+v", got)
	}
	f.insert("github.com/Elagoht/collage-consent", "consent", func(id string) string { return id + ".New()" }, 1)
	out, _ := f.format()
	s := string(out)
	wantInOrder(t, s, `base := "https://example.com" // a statement of the user's own`,
		"// the sitemap needs the site's address", "sitemap.New(sitemap.Options{BaseURL: base}),",
		"consent.New(),", "devtoolbar.New(),")

	g, _ := parsePluginsFile(out)
	if !g.remove("github.com/elagoht/collage-consent") { // case-insensitive
		t.Fatal("remove found nothing")
	}
	back, _ := g.format()
	if string(back) != edited {
		t.Fatalf("add then remove changed the file:\n%s", back)
	}
}

// Review Focus 5: a package name already taken is aliased.
func TestPluginsFile_NameClash(t *testing.T) {
	f, _ := parsePluginsFile(nil)
	call := func(id string) string { return id + ".New()" }
	a := f.insert("github.com/Elagoht/collage-live", "live", call, 0)
	b := f.insert("github.com/someone/collage-live", "live", call, 1)
	c := f.insert("github.com/x/collage-collage", "collage", call, 2)
	if a != "live" || b != "someoneLive" || c != "xCollage" {
		t.Fatalf("idents %q %q %q", a, b, c)
	}
	d := f.insert("github.com/someone/collage-live-two", "live", call, 3) // live and someoneLive are taken
	if d != "someoneLive2" {
		t.Fatalf("third live = %q, want someoneLive2", d)
	}
	if _, err := f.format(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertIndex(t *testing.T) {
	for _, tc := range []struct {
		orders []int
		order  int
		want   int
	}{
		{nil, 0, 0},
		{[]int{0, 0, 100}, 0, 2},
		{[]int{0, 100}, -30, 0},
		{[]int{-30, 0}, -25, 1},
		{[]int{0, 0}, 100, 2},
		{[]int{100, 0}, 0, 0}, // a hand-reordered list: before the first greater one
	} {
		if got := insertIndex(tc.orders, tc.order); got != tc.want {
			t.Errorf("%v + %d = %d, want %d", tc.orders, tc.order, got, tc.want)
		}
	}
}

func TestPluginsFile_Refuses(t *testing.T) {
	for name, src := range map[string]string{
		"no func":       "package main\n",
		"not a literal": "package main\nimport \"github.com/Elagoht/collage/pkg/collage\"\nfunc plugins() []collage.Plugin { return nil }\n",
		"syntax":        "package main\nfunc plugins( {",
	} {
		if _, err := parsePluginsFile([]byte(src)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
```

The alias rule: the package name if free; otherwise owner + the package name
with its first letter capitalised, where the owner is the path segment after the
host, lowercased; if that is taken too, append 2, 3, …. An "import name taken"
check covers every import in the file, `collage` included.

- [ ] **Step 2: Write the failing tests for `plugins-config.json`**

```go
package cli

import "testing"

func TestAddConfigSection(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"absent file", "", "{\n  \"a/b\": {}\n}\n"},
		{"empty object", "{}", "{\n  \"a/b\": {}\n}"},
		{"two spaces", "{\n  \"x/y\": {\"k\": 1}\n}\n", "{\n  \"x/y\": {\"k\": 1},\n  \"a/b\": {}\n}\n"},
		{"tabs", "{\n\t\"x/y\": {}\n}", "{\n\t\"x/y\": {},\n\t\"a/b\": {}\n}"},
		{"crlf", "{\r\n  \"$schema\": \"s\"\r\n}\r\n", "{\r\n  \"$schema\": \"s\",\r\n  \"a/b\": {}\r\n}\r\n"},
		{"one line", `{"x/y": {}}`, `{"x/y": {}, "a/b": {}}`},
	} {
		var src []byte
		if tc.in != "" {
			src = []byte(tc.in)
		}
		out, added, err := addConfigSection(src, "a/b")
		if err != nil || !added || string(out) != tc.want {
			t.Errorf("%s: %q, %v, %v; want %q", tc.name, out, added, err, tc.want)
		}
	}
	keep := []byte("{\n  \"a/b\": {\"k\": 2}\n}\n")
	if out, added, err := addConfigSection(keep, "a/b"); err != nil || added || string(out) != string(keep) {
		t.Errorf("existing section touched: %q %v %v", out, added, err)
	}
	for _, bad := range []string{"[]", "1", "{", `{"a":1} trailing`} {
		if _, _, err := addConfigSection([]byte(bad), "a/b"); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestRemoveConfigSection(t *testing.T) {
	for _, tc := range []struct{ name, in, want, removed string }{
		{"last", "{\n  \"x/y\": {},\n  \"a/b\": {\"k\": 1}\n}\n", "{\n  \"x/y\": {}\n}\n", `{"k": 1}`},
		{"first", "{\n  \"a/b\": {},\n  \"x/y\": {}\n}\n", "{\n  \"x/y\": {}\n}\n", "{}"},
		{"only", "{\n  \"a/b\": {}\n}\n", "{}\n", "{}"},
		{"crlf", "{\r\n  \"x/y\": {},\r\n  \"a/b\": {}\r\n}\r\n", "{\r\n  \"x/y\": {}\r\n}\r\n", "{}"},
		{"one line", `{"x/y": {}, "a/b": {}}`, `{"x/y": {}}`, "{}"},
	} {
		out, removed, err := removeConfigSection([]byte(tc.in), "a/b")
		if err != nil || string(out) != tc.want || string(removed) != tc.removed {
			t.Errorf("%s: %q %q %v; want %q %q", tc.name, out, removed, err, tc.want, tc.removed)
		}
	}
	src := []byte("{\n  \"x/y\": {}\n}\n")
	if out, removed, err := removeConfigSection(src, "a/b"); err != nil || removed != nil || string(out) != string(src) {
		t.Errorf("absent section: %q %q %v", out, removed, err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test -count=1 -run 'PluginsFile|InsertIndex|ConfigSection' ./internal/cli/`
Expected: FAIL, build errors (undefined `parsePluginsFile`, `addConfigSection`)

- [ ] **Step 4: Implement `pluginsgo.go`**

Requirements, in this order:
- The skeleton for `nil` src is the exact text of `scaffold/common/plugins.go.tmpl` from Task 7. Keep it as a Go const `pluginsSkeleton` here, and have Task 7's template file hold the same text. A test in Task 7 asserts they are equal.
- Parse with `parser.ParseComments`. Find `func plugins() []collage.Plugin`. Its body's last statement must be `return <CompositeLit>`; otherwise return an error, `plugins.go: func plugins must end in "return []collage.Plugin{…}"; collage plugin edits only that list`.
- An element belongs to an import when it is a `*ast.CallExpr` whose `Fun` is a `*ast.SelectorExpr` with an `*ast.Ident` X naming an import, either by its explicit name or by the last path element when unnamed.
- `insert`: compute the ident (Task 3's alias rule). Add the import as a named import (`astutil` is not stdlib; append an `*ast.ImportSpec` to the first import `GenDecl` yourself). Parse the call text with `parser.ParseExpr(call(ident))` and insert it into `CompositeLit.Elts`.
- `remove`: delete every element attributed to the import, and the import spec.
- `format`: `format.Node` with the file set.
- Comments: deleting nodes in go/ast can strand comments. Use `ast.CommentMap`: build it, edit, then set `file.Comments = cmap.Filter(file).Comments()`. That is what makes `TestPluginsFile_KeepsTheUsersEdits` round-trip. Positions of newly parsed expressions are `token.NoPos`. If gofmt puts the new element on the previous element's line, fix it by giving the new element the position of the closing brace minus one line. Simpler and robust: build the output by re-parsing. After the edits, `format.Node` into a buffer, `parser.ParseFile` that buffer again, and `format.Node` once more. The second pass normalises layout. Keep whatever approach passes the round-trip test, and say which one in the report.

- [ ] **Step 5: Implement `pluginconfig.go`**

- Find the top-level members with `json.Decoder` over the bytes, using `Token()` and `InputOffset()`.
  - The first token must be `json.Delim('{')`; otherwise error `plugins-config.json is not a JSON object`.
  - For each member, record the offset of its key's opening quote, the offset after its value, and the decoded key.
  - The closing `}` offset is where the decoder sits after the last value; scan forward over whitespace to the `}`.
  - After the object only whitespace may follow; otherwise error.
- **Line ending:** `"\r\n"` if the source contains `"\r\n"`, else `"\n"`.
- **Indentation:** the whitespace between the line start and the first member's key. If there is no member, two spaces. If the object is on one line (no newline between `{` and `}`), use the inline form `, "a/b": {}`.
- **Add:**
  - with members: insert `"," + nl + indent + "\"a/b\": {}"` right after the last member's value;
  - empty `{}` (or `{` whitespace `}`): replace the inside with `nl + indent + "\"a/b\": {}" + nl`, and keep what follows the `}` unchanged;
  - `src == nil`: the result is `"{\n  \"a/b\": {}\n}\n"`.
- **Remove:**
  - a middle or first member: cut from its key's start to the start of the next member's key;
  - the last of several: cut from the end of the previous member's value to the end of its own value;
  - the only member: replace the inside with nothing, giving `{}`.
  - `removed` is `src[valueStart:valueEnd]`, trimmed of surrounding whitespace.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -count=1 -run 'PluginsFile|InsertIndex|ConfigSection' ./internal/cli/ && go vet ./internal/cli/`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/cli/pluginsgo.go internal/cli/pluginsgo_test.go internal/cli/pluginconfig.go internal/cli/pluginconfig_test.go
git commit -m "feat(cli): editors for plugins.go and plugins-config.json that keep the user's text"
```

---

### Task 4: fake module proxy, go tool helpers, and `plugin add` (main path)

**Files:**
- Create: `internal/cli/fakeproxy_test.go`, `internal/cli/gotool.go`, `internal/cli/plugin.go`
- Modify: `internal/cli/cli.go`. Add `{"plugin", "Add, remove or check a plugin: plugin add owner/name"}` to `builtins` after `"add"`, a `case "plugin": return c.runPlugin(ctx, rest)` in `Run`, and `case "plugin": fmt.Fprint(w, pluginUsage)` in `printCommandHelp`. Update the `ErrReservedCommandName` doc comment's list.
- Test: `internal/cli/plugin_test.go`

**Interfaces:**
- Consumes: Task 2 (`plugincontract.ParseRef`, `Check`, `IsPseudoVersion`, `ConventionalPath`), Task 3 (all editors).
- Produces (for Tasks 5–7):

```go
// gotool.go
func (c *CLI) goOutput(ctx context.Context, dir string, args ...string) (stdout, stderr []byte, err error)
type moduleInfo struct{ Path, Version, Dir string }
func (c *CLI) listModule(ctx context.Context, dir, path string) (moduleInfo, error) // go list -m -json <path>
type fileSnapshot map[string][]byte // absolute path → content; nil = was absent
func snapshot(paths ...string) (fileSnapshot, error)
func (s fileSnapshot) restore() error
// plugin.go
const pluginUsage = "…"
func (c *CLI) runPlugin(ctx context.Context, args []string) int
func (c *CLI) pluginAdd(ctx context.Context, dir string, ref plugincontract.Ref, visiting map[string]bool) (added []addedPlugin, err error)
type addedPlugin struct{ Name, Version, Call string; Setup string }
// fakeproxy_test.go
type fakeModule struct {
	Path    string            // the path go.mod declares
	Served  string            // the path the proxy serves it under; "" means Path
	Version string
	Files   map[string]string // relative name → content; go.mod is generated from Path when absent
}
func fakeProxy(t *testing.T, mods ...fakeModule) // writes the proxy and sets every env var of Global Constraints
func fixturePlugin(owner, name, version, newDecl string, manifestExtra string) fakeModule
func pluginProject(t *testing.T) string // a scaffolded minimal project whose collage is replaced by this repo
```

- [ ] **Step 1: Write the fake proxy**

`fakeproxy_test.go`:

```go
package cli

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

type fakeModule struct {
	Path    string
	Served  string
	Version string
	Files   map[string]string
}

// escapePath is the module proxy's case encoding: an upper-case letter is "!"
// and its lower case, so the path is safe on a case-insensitive file system.
func escapePath(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// fakeProxy writes mods as a GOPROXY=file:// tree and points the go tool at it,
// and at nothing else: no checksum database, a module cache of the test's own.
func fakeProxy(t *testing.T, mods ...fakeModule) {
	t.Helper()
	root := t.TempDir()
	lists := map[string][]string{}
	for _, m := range mods {
		served := m.Served
		if served == "" {
			served = m.Path
		}
		dir := filepath.Join(root, filepath.FromSlash(escapePath(served)), "@v")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{}
		for k, v := range m.Files {
			files[k] = v
		}
		if _, ok := files["go.mod"]; !ok {
			files["go.mod"] = fmt.Sprintf("module %s\n\ngo 1.26\n\nrequire github.com/Elagoht/collage v0.57.0\n", m.Path)
		}
		var zbuf bytes.Buffer
		zw := zip.NewWriter(&zbuf)
		for name, body := range files {
			w, err := zw.Create(m.Path + "@" + m.Version + "/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		v := escapePath(m.Version)
		for name, body := range map[string][]byte{
			v + ".info": []byte(fmt.Sprintf(`{"Version":%q,"Time":"2026-10-01T00:00:00Z"}`, m.Version)),
			v + ".mod":  []byte(files["go.mod"]),
			v + ".zip":  zbuf.Bytes(),
		} {
			if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		lists[dir] = append(lists[dir], m.Version)
	}
	for dir, versions := range lists {
		if err := os.WriteFile(filepath.Join(dir, "list"), []byte(strings.Join(versions, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(root))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GONOSUMDB", "*")
	t.Setenv("GOFLAGS", "-mod=mod -modcacherw")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOWORK", "off")
}

// fixturePlugin is a minimal plugin module: a collage.json naming owner/name
// (with manifestExtra spliced in as further members, e.g. `"order": 100`), and
// a package whose New is newDecl.
func fixturePlugin(owner, name, version, newDecl, manifestExtra string) fakeModule {
	pkg := strings.ReplaceAll(name, "-", "")
	manifest := fmt.Sprintf(`{"name": %q`, owner+"/"+name)
	if manifestExtra != "" {
		manifest += ", " + manifestExtra
	}
	manifest += "}\n"
	src := fmt.Sprintf(`package %s

import (
	"context"

	"github.com/Elagoht/collage/pkg/collage"
)

type Options struct {
	Greeting string `+"`json:\"greeting\"`"+`
}

type Plugin struct{ opts Options }

%s

func (p *Plugin) Name() string    { return %q }
func (p *Plugin) Version() string { return %q }
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	opts, err := collage.PluginConfig(host, p.opts)
	p.opts = opts
	return err
}
func (p *Plugin) Shutdown(context.Context) error { return nil }
`, pkg, newDecl, owner+"/"+name, strings.TrimPrefix(version, "v"))
	return fakeModule{
		Path:    "github.com/" + owner + "/collage-" + name,
		Version: version,
		Files:   map[string]string{"collage.json": manifest, "plugin.go": src},
	}
}
```

`pluginProject` scaffolds with `c.Run(…"new", "app", "--template", "minimal", "-dir", dir, "-module", "pluginapp")`.
It then runs `go mod edit -replace github.com/Elagoht/collage=<repoRoot>` and
`go mod edit -require github.com/Elagoht/collage@v0.57.0` in the dir, with
`exec.Command` and the env set by `fakeProxy` (call `fakeProxy` first). Copy the
repo-root computation from `add_test.go`.

- [ ] **Step 2: Write the failing tests for `add`**

```go
package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func plugin(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	c, out, errOut := testCLI()
	code := c.Run(context.Background(), append(append([]string{"plugin"}, args...), "-dir", dir))
	return code, out.String(), errOut.String()
}

func needGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not available")
	}
}

func goBuild(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
}

func TestPluginAdd(t *testing.T) {
	needGo(t)
	fakeProxy(t,
		fixturePlugin("acme", "greet", "v0.1.0", "func New(opts Options) *Plugin { return &Plugin{opts: opts} }", ""),
		fixturePlugin("acme", "plain", "v0.2.0", "func New() *Plugin { return &Plugin{} }", `"setup": "Pass it nothing."`),
	)
	dir := pluginProject(t)

	code, out, errOut := plugin(t, dir, "add", "acme/greet")
	if code != 0 {
		t.Fatalf("add = %d\n%s%s", code, out, errOut)
	}
	wantInOrder(t, out, "added acme/greet v0.1.0", "plugins.go", "greet.New(greet.Options{})",
		"plugins-config.json", `"acme/greet": {}`)
	if code, out, errOut := plugin(t, dir, "add", "acme/plain"); code != 0 || !strings.Contains(out, "Pass it nothing.") {
		t.Fatalf("add plain = %d\n%s%s", code, out, errOut)
	}
	wantInOrder(t, readFile(t, dir, "plugins.go"), `greet "github.com/acme/collage-greet"`, `plain "github.com/acme/collage-plain"`,
		"greet.New(greet.Options{}),", "plain.New(),")
	wantInOrder(t, readFile(t, dir, "plugins-config.json"), `"acme/greet": {}`, `"acme/plain": {}`)
	wantInOrder(t, readFile(t, dir, "go.mod"), "github.com/acme/collage-greet v0.1.0", "github.com/acme/collage-plain v0.2.0")
	goBuild(t, dir)
}

// Review Focus 3: a module that breaks the contract leaves every file as it was.
func TestPluginAdd_RefusedRestoresEverything(t *testing.T) {
	needGo(t)
	broken := fixturePlugin("acme", "broken", "v0.1.0", "func New(a, b string) *Plugin { return nil }", "")
	nomanifest := fixturePlugin("acme", "bare", "v0.1.0", "func New() *Plugin { return nil }", "")
	delete(nomanifest.Files, "collage.json")
	fakeProxy(t, broken, nomanifest)
	dir := pluginProject(t)
	before := map[string]string{}
	for _, f := range []string{"go.mod", "go.sum", "plugins.go", "plugins-config.json"} {
		if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
			before[f] = string(b)
		}
	}
	for _, ref := range []string{"acme/broken", "acme/bare"} {
		code, _, errOut := plugin(t, dir, "add", ref)
		if code != 1 || !strings.Contains(errOut, "collage: plugin add: ") {
			t.Fatalf("%s: code %d\n%s", ref, code, errOut)
		}
		for _, f := range []string{"go.mod", "go.sum", "plugins.go", "plugins-config.json"} {
			b, err := os.ReadFile(filepath.Join(dir, f))
			was, existed := before[f]
			if existed != (err == nil) || (existed && string(b) != was) {
				t.Errorf("%s: %s changed", ref, f)
			}
		}
	}
}
```

The minimal scaffold has a `plugins.go` and a `plugins-config.json` only after
Task 7. Until then they are absent, and the test's `before` map handles both
cases. Put a hint test here too: with no `plugins()` call in the package, the
output contains `plugins.go is new: give it to collage.New once` and
`Plugins: plugins(),`. Assert it in `TestPluginAdd` while the scaffold lacks the
call. Task 7 flips that assertion: the scaffold then calls `plugins()`, so the
hint must not appear.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test -count=1 -run 'PluginAdd' ./internal/cli/`
Expected: FAIL, `collage: unknown command: "plugin"` (exit 2), or build errors for `runPlugin`

- [ ] **Step 4: Implement `gotool.go`**

```go
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

// goOutput runs the go tool in dir and returns what it printed, for the
// commands whose output is read rather than shown.
func (c *CLI) goOutput(ctx context.Context, dir string, args ...string) ([]byte, []byte, error) {
	var out, errOut bytes.Buffer
	err := c.runner().Run(ctx, dir, nil, &out, &errOut, "go", args...)
	return out.Bytes(), errOut.Bytes(), err
}

type moduleInfo struct {
	Path    string
	Version string
	Dir     string
}

// listModule is "go list -m -json path" in dir: where the module's source sits
// in the module cache, and at which version.
func (c *CLI) listModule(ctx context.Context, dir, path string) (moduleInfo, error) {
	out, errOut, err := c.goOutput(ctx, dir, "list", "-m", "-json", path)
	if err != nil {
		return moduleInfo{}, errors.New(string(bytes.TrimSpace(errOut)))
	}
	var m moduleInfo
	if err := json.Unmarshal(out, &m); err != nil {
		return moduleInfo{}, err
	}
	return m, nil
}

// fileSnapshot is the content of files before a change, so a failed change
// can put them back: nil for a file that did not exist.
type fileSnapshot map[string][]byte

func snapshot(paths ...string) (fileSnapshot, error) {
	s := fileSnapshot{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			s[p] = nil
		case err != nil:
			return nil, err
		default:
			s[p] = b
		}
	}
	return s, nil
}

// restore writes every file back as it was, and removes those that were absent.
func (s fileSnapshot) restore() error {
	var errs []error
	for p, b := range s {
		if b == nil {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
```

Check the real `CommandRunner` (`internal/cli/dev.go`) for how `dir == ""` and
`env` behave. The tests rely on the process environment, which `t.Setenv` sets,
reaching the go tool. Confirm the real runner appends `env` to `os.Environ()`
rather than replacing it.

- [ ] **Step 5: Implement `plugin.go` (dispatch and the main `add` path)**

The usage text, exactly:

```go
const pluginUsage = `Usage:
  collage plugin add <owner/name | module path>[@version] [-dir dir]
  collage plugin del <owner/name | module path> [-dir dir]
  collage plugin check [-release] [-run] [dir]

add fetches a plugin with go get, checks that it is one, and adds it to
plugins.go and an empty section for it to plugins-config.json. owner/name means
github.com/<owner>/collage-<name>; a full module path is used as written.

del removes the plugin from plugins.go and plugins-config.json, printing a
section that held settings, and runs go mod tidy.

check checks a plugin repository against the contract add enforces; with
-release it also wants a release tag at HEAD, and with -run it builds the
plugin into a throwaway application.

  -dir   the directory of the project's package main (default ".")

The contract: ` + plugincontract.AuthorsURL + `
`
```

`runPlugin` dispatches on `args[0]`: `add`, `del`, `check`, or help. Anything
else is exit 2 with the usage. Parse `-dir` with `splitPositional`, as
`runAdd` does. `add` and `del` take exactly one positional argument.

`add`, main path (Task 5 adds the rest):
1. Check that `dir` has a non-test `.go` file declaring `package main`. Otherwise fail with `collage: plugin add: <dir> holds no package main; pass the project's directory with -dir`.
2. `snap := snapshot(dir/go.mod, dir/go.sum, dir/plugins.go, dir/plugins-config.json)`. go.mod is looked for in `dir` only; a project whose go.mod is elsewhere is out of scope. Fail with `no go.mod in <dir>`.
3. `go get <path>@<version or latest>` via `goOutput`. On error, restore, then fail with go's stderr trimmed.
4. `listModule(dir, path)`. Then `plugincontract.Check(info.Dir, ref.Short)`. On error, restore and fail. Also refuse with restore when `plugincontract.IsPseudoVersion(info.Version)`, with `<path> <version> is not a release; tag one (git tag v0.1.0) and push it`.
5. Load `plugins.go`; nil if absent. For each existing entry, get its order. For `listModule` of the entry's import path: on error or `ReadManifest` error, use order 0. `at := insertIndex(orders, manifest.Order)`. Then `insert(info.Path, constructor.Package, constructor.Call, at)`. Write `format()`.
6. Run `addConfigSection` on `plugins-config.json` and write it if added.
7. `go mod tidy`. On error, restore and fail with its stderr.
8. Print exactly this, aligning with `tabwriter` like `printUsage`:
   ```
   added <name> <version>
     plugins.go           <call>
     plugins-config.json  "<name>": {}       (or: "<name>" kept as it was)
   ```
   Then the `setup` note, if any, on its own paragraph. Then the `plugins()` hint (see Step 2) when no non-test file other than `plugins.go` in `dir` contains a call expression `plugins()`.

Every failure prints `collage: plugin add: <reason>` to stderr and returns 1.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -count=1 -run 'PluginAdd|PluginsFile|ConfigSection' ./internal/cli/ && go vet ./internal/cli/...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/cli/fakeproxy_test.go internal/cli/gotool.go internal/cli/plugin.go internal/cli/plugin_test.go internal/cli/cli.go
git commit -m "feat(cli): collage plugin add fetches, checks and wires a plugin, or changes nothing"
```

---

### Task 5: `add`: versions, requires, already present, mixed case, tidy failure

**Files:**
- Modify: `internal/cli/plugin.go`
- Test: `internal/cli/plugin_test.go` (append)

**Interfaces:**
- Consumes: Task 4's `pluginAdd`, `fakeProxy`, `fixturePlugin`, `pluginProject`, `plugin`, `goBuild`.

- [ ] **Step 1: Write the failing tests**

```go
func TestPluginAdd_Version(t *testing.T) {
	needGo(t)
	fakeProxy(t,
		fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""),
		fixturePlugin("acme", "greet", "v0.2.0", "func New() *Plugin { return &Plugin{} }", ""),
	)
	dir := pluginProject(t)
	if code, _, e := plugin(t, dir, "add", "acme/greet@v0.1.0"); code != 0 {
		t.Fatal(e)
	}
	code, out, e := plugin(t, dir, "add", "acme/greet")
	if code != 0 || !strings.Contains(out, "acme/greet is already added; now at v0.2.0") {
		t.Fatalf("%d %s %s", code, out, e)
	}
	if strings.Count(readFile(t, dir, "plugins.go"), "greet.New()") != 1 {
		t.Fatal("a second entry")
	}
	if code, out, _ := plugin(t, dir, "add", "acme/greet"); code != 0 || !strings.Contains(out, "acme/greet is already added") || strings.Contains(out, "now at") {
		t.Fatalf("no-op add: %d %s", code, out)
	}
}

func TestPluginAdd_PseudoVersionRefused(t *testing.T) {
	needGo(t)
	fakeProxy(t, fixturePlugin("acme", "greet", "v0.0.0-20260101000000-abcdefabcdef", "func New() *Plugin { return &Plugin{} }", ""))
	dir := pluginProject(t)
	code, _, e := plugin(t, dir, "add", "acme/greet@v0.0.0-20260101000000-abcdefabcdef")
	if code != 1 || !strings.Contains(e, "is not a release") {
		t.Fatalf("%d %s", code, e)
	}
	if strings.Contains(readFile(t, dir, "go.mod"), "collage-greet") {
		t.Fatal("go.mod kept the refused module")
	}
}

func TestPluginAdd_Requires(t *testing.T) {
	needGo(t)
	fakeProxy(t,
		fixturePlugin("acme", "base", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""),
		fixturePlugin("acme", "ext", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/base"], "order": -1`),
		fixturePlugin("acme", "loop-a", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/loop-b"]`),
		fixturePlugin("acme", "loop-b", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/loop-a"]`),
	)
	dir := pluginProject(t)
	code, out, e := plugin(t, dir, "add", "acme/ext")
	if code != 0 {
		t.Fatal(e)
	}
	wantInOrder(t, out, "added acme/base", "added acme/ext")
	wantInOrder(t, readFile(t, dir, "plugins.go"), "ext.New(),", "base.New(),") // ext's order -1 puts it first
	goBuild(t, dir)
	if code, _, e := plugin(t, dir, "add", "acme/loop-a"); code != 1 || !strings.Contains(e, "cycle") {
		t.Fatalf("cycle: %d %s", code, e)
	}
}

// Review Focus 4: typed in mixed case, served lowercase, declared mixed.
func TestPluginAdd_MixedCase(t *testing.T) {
	needGo(t)
	m := fixturePlugin("Acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", "")
	m.Files["collage.json"] = `{"name": "acme/greet"}` + "\n"
	m.Files["plugin.go"] = strings.Replace(m.Files["plugin.go"], `"Acme/greet"`, `"acme/greet"`, 1)
	lower := m
	lower.Served = "github.com/acme/collage-greet"
	fakeProxy(t, m, lower)
	dir := pluginProject(t)
	code, out, e := plugin(t, dir, "add", "Acme/Greet")
	if code != 0 || !strings.Contains(readFile(t, dir, "go.mod"), "github.com/Acme/collage-greet v0.1.0") {
		t.Fatalf("%d %s %s", code, out, e)
	}
	goBuild(t, dir)
}

// Review Focus 3: go mod tidy fails after every file was written.
func TestPluginAdd_TidyFailureRestores(t *testing.T) {
	needGo(t)
	bad := fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", "")
	bad.Files["go.mod"] = "module github.com/acme/collage-greet\n\ngo 1.26\n\nrequire github.com/Elagoht/collage v0.57.0\nrequire example.invalid/missing v1.0.0\n"
	fakeProxy(t, bad)
	dir := pluginProject(t)
	before := readFile(t, dir, "go.mod")
	if code, _, e := plugin(t, dir, "add", "acme/greet"); code != 1 {
		t.Fatalf("add succeeded with a broken dependency: %s", e)
	}
	if readFile(t, dir, "go.mod") != before {
		t.Fatal("go.mod not restored")
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins.go")); err == nil && !strings.Contains(readFile(t, dir, "plugins.go"), "func plugins") {
		t.Fatal("plugins.go left half-written")
	}
}
```

If `go get` itself fails on `example.invalid/missing` before tidy runs, the test
still proves the restore. Then find another way to make only tidy fail: for
example, write a `.go` file into the project after `go get` through a test hook.
Prefer a fixture whose `go get` succeeds and whose `go mod tidy` fails, such as
a plugin package importing a package that does not exist in a module that does.
Record which route was used in the report.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 -run 'PluginAdd_(Version|Pseudo|Requires|MixedCase|Tidy)' ./internal/cli/`
Expected: FAIL. Version: a duplicate entry or no "already added". Requires: base not added. MixedCase: go get fails with "module declares its path as".

- [ ] **Step 3: Implement**

- **Already present:**
  - Before editing `plugins.go`, if `f.has(info.Path)` holds, skip steps 5 and 6 of Task 4.
  - Compare the go.mod requirement version read before `go get` with `info.Version`. Print `<name> is already added; now at <version>` if it differs, else `<name> is already added`.
  - Still run `go mod tidy`.
- **Mixed case:**
  - If `go get` fails and stderr matches `module declares its path as: (\S+)`, and `strings.EqualFold(match, path)`, retry once with the match.
  - Use the declared path from then on, for `listModule`, the import and the config.
- **Requires:**
  - `pluginAdd` takes `visiting map[string]bool` keyed by manifest name.
  - After `Check`, for each name in `Manifest.Requires` not already attributable in `plugins.go`, call `pluginAdd` recursively with `plugincontract.ConventionalPath(name)` and `Short: name`, before inserting this plugin.
  - A name already in `visiting` returns `cycle: a requires b requires a`.
  - Collect `[]addedPlugin` in the order added and print one block per plugin.
  - The snapshot is taken once, by the outermost call. Any failure anywhere restores all of it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 -run 'PluginAdd' ./internal/cli/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cli/plugin.go internal/cli/plugin_test.go
git commit -m "feat(cli): plugin add follows requires, upgrades in place, and tolerates a mixed-case path"
```

---

### Task 6: `plugin del`

**Files:**
- Modify: `internal/cli/plugin.go`
- Test: `internal/cli/plugin_test.go` (append)

**Interfaces:**
- Consumes: Task 3's `remove`/`removeConfigSection`, and Task 4's helpers.

- [ ] **Step 1: Write the failing tests**

```go
func TestPluginDel(t *testing.T) {
	needGo(t)
	fakeProxy(t,
		fixturePlugin("acme", "base", "v0.1.0", "func New() *Plugin { return &Plugin{} }", ""),
		fixturePlugin("acme", "ext", "v0.1.0", "func New() *Plugin { return &Plugin{} }", `"requires": ["acme/base"]`),
		fixturePlugin("acme", "greet", "v0.1.0", "func New(o Options) *Plugin { return &Plugin{opts: o} }", ""),
	)
	dir := pluginProject(t)
	for _, ref := range []string{"acme/ext", "acme/greet"} {
		if code, _, e := plugin(t, dir, "add", ref); code != 0 {
			t.Fatal(e)
		}
	}
	// Settings typed into the section are printed when it goes.
	cfg := strings.Replace(readFile(t, dir, "plugins-config.json"), `"acme/greet": {}`, `"acme/greet": {"greeting": "selam"}`, 1)
	writeFiles(t, dir, map[string]string{"plugins-config.json": cfg})

	if code, _, e := plugin(t, dir, "del", "acme/base"); code != 1 || !strings.Contains(e, "acme/ext requires acme/base; remove it first") {
		t.Fatalf("dependent: %d %s", code, e)
	}
	code, out, e := plugin(t, dir, "del", "ACME/Greet") // Review Focus 4
	if code != 0 || !strings.Contains(out, `{"greeting": "selam"}`) {
		t.Fatalf("%d %s %s", code, out, e)
	}
	for _, f := range []string{"plugins.go", "plugins-config.json", "go.mod"} {
		if strings.Contains(strings.ToLower(readFile(t, dir, f)), "greet") {
			t.Errorf("%s still names greet", f)
		}
	}
	if code, _, e := plugin(t, dir, "del", "github.com/acme/collage-ext"); code != 0 {
		t.Fatal(e)
	}
	if code, _, e := plugin(t, dir, "del", "acme/nope"); code != 1 || !strings.Contains(e, "acme/base") {
		t.Fatalf("unknown: %d %s (the error lists what plugins.go has)", code, e)
	}
	goBuild(t, dir)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 -run PluginDel ./internal/cli/`
Expected: FAIL, unknown subcommand `del` (exit 2)

- [ ] **Step 3: Implement `del`**

1. **Validate.** Check `package main` and snapshot as in `add`. If `plugins.go` is absent, fail with `no plugins.go in <dir>; nothing to remove`.
2. **Resolve each entry.** For each entry, `listModule(dir, import)` and `ReadManifest(info.Dir)`. Either may fail; then the entry has only its import path.
3. **Match the target:**
   - a full ref matches an entry whose import path equals it case-insensitively;
   - a short ref matches an entry whose manifest name equals it, or whose import path equals `ConventionalPath(short)` case-insensitively;
   - with no match, fail with `<ref> is not in plugins.go; it has: <names or paths, comma-separated>`.
4. **Dependents.** If another entry's manifest `Requires` contains the target's name, fail with `<other> requires <target>; remove it first`.
5. **Edit and print.**
   - Run `remove` on `plugins.go` and `removeConfigSection` on `plugins-config.json`, then write both.
   - Print `removed <name>`. When `removed` is not `{}` (compare after `json.Compact`), also print `its settings were:` and the raw value, so they are not lost.
6. **Tidy.** Run `go mod tidy`, and restore on failure.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 -run 'PluginDel|PluginAdd' ./internal/cli/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cli/plugin.go internal/cli/plugin_test.go
git commit -m "feat(cli): collage plugin del removes a plugin and keeps its settings on screen"
```

---

### Task 7: `plugin check`, and the scaffold's `plugins.go`

**Files:**
- Create: `internal/cli/plugincheck.go`, `internal/cli/scaffold/common/plugins.go.tmpl`
- Modify: `internal/cli/scaffold/common/main.go.tmpl`. Replace the commented `Plugins:` block with `Plugins: plugins(),`, plus a two-line comment: plugins.go is managed by `collage plugin add`, and settings are in plugins-config.json.
- Modify: `internal/cli/plugin.go` (dispatch `check`)
- Modify: `internal/cli/new_test.go` (`TestRun_New_ScaffoldsExpectedFiles` lists `plugins.go`)
- Test: `internal/cli/plugincheck_test.go`, and the hint assertion in `plugin_test.go`

**Interfaces:**
- Consumes: Task 2 `Check`, `IsPseudoVersion`; Task 3 `pluginsSkeleton`; Task 4 helpers.
- Produces: `func (c *CLI) pluginCheck(ctx context.Context, dir string, release, run bool) error`

- [ ] **Step 1: Write the failing tests**

```go
package cli

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPluginCheck_Static(t *testing.T) {
	good := fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", "")
	dir := t.TempDir()
	writeFiles(t, dir, good.Files)
	writeFiles(t, dir, map[string]string{"go.mod": "module github.com/acme/collage-greet\n\ngo 1.26\n"})
	c, out, errOut := testCLI()
	if code := c.Run(t.Context(), []string{"plugin", "check", dir}); code != 0 {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if !strings.Contains(out.String(), "acme/greet: meets the contract") {
		t.Fatal(out.String())
	}
	writeFiles(t, dir, map[string]string{"collage.json": `{"name": "Bad"}`})
	c, _, errOut = testCLI()
	if code := c.Run(t.Context(), []string{"plugin", "check", dir}); code != 1 || !strings.Contains(errOut.String(), "lowercase") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestPluginCheck_Release(t *testing.T) {
	needGo(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	good := fixturePlugin("acme", "greet", "v0.1.0", "func New() *Plugin { return &Plugin{} }", "")
	dir := t.TempDir()
	writeFiles(t, dir, good.Files)
	writeFiles(t, dir, map[string]string{"go.mod": "module github.com/acme/collage-greet\n\ngo 1.26\n"})
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-qm", "x")
	c, _, errOut := testCLI()
	if code := c.Run(t.Context(), []string{"plugin", "check", "-release", dir}); code != 1 || !strings.Contains(errOut.String(), "no release tag at HEAD") {
		t.Fatalf("untagged: %d %s", code, errOut)
	}
	git("tag", "v0.1.0")
	c, _, errOut = testCLI()
	if code := c.Run(t.Context(), []string{"plugin", "check", "-release", dir}); code != 0 {
		t.Fatalf("tagged: %s", errOut)
	}
}

func TestPluginCheck_Run(t *testing.T) {
	needGo(t)
	fakeProxy(t) // offline env, no modules needed
	for _, tc := range []struct {
		name, decl, want string
		code             int
	}{
		{"works", "func New() *Plugin { return &Plugin{} }", "acme/greet: builds and starts", 0},
		{"options", "func New(o Options) *Plugin { return &Plugin{opts: o} }", "acme/greet: builds and starts", 0},
		{"init fails", "func New() *Plugin { return &Plugin{} }\nfunc init() { failInit = true }\nvar failInit bool", "init", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := fixturePlugin("acme", "greet", "v0.1.0", tc.decl, "")
			if tc.name == "init fails" {
				m.Files["plugin.go"] = strings.Replace(m.Files["plugin.go"], "opts, err := collage.PluginConfig(host, p.opts)",
					"if failInit { return errors.New(\"refuses an empty config\") }\n\topts, err := collage.PluginConfig(host, p.opts)", 1)
				m.Files["plugin.go"] = strings.Replace(m.Files["plugin.go"], `"context"`, "\"context\"\n\t\"errors\"", 1)
			}
			dir := t.TempDir()
			writeFiles(t, dir, m.Files)
			writeFiles(t, dir, map[string]string{"go.mod": "module github.com/acme/collage-greet\n\ngo 1.26\n\nrequire github.com/Elagoht/collage v0.57.0\n\nreplace github.com/Elagoht/collage => " + repoRootForTest(t) + "\n"})
			c, out, errOut := testCLI()
			code := c.Run(t.Context(), []string{"plugin", "check", "-run", dir})
			if code != tc.code || !strings.Contains(out.String()+errOut.String(), tc.want) {
				t.Fatalf("%d\n%s\n%s", code, out, errOut)
			}
		})
	}
}
```

Add `repoRootForTest(t)` to `fakeproxy_test.go`, factored out of
`pluginProject`, and use it in both places.

Scaffold test: in `new_test.go`'s `TestRun_New_Scaffold_Compiles`, or the
minimal variant, assert that `plugins.go` exists and equals `pluginsSkeleton`,
and that `main.go` contains `Plugins: plugins(),`. In
`plugin_test.go`'s `TestPluginAdd`, flip the hint assertion: the output must NOT
contain `plugins.go is new`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 -run 'PluginCheck|TestRun_New|PluginAdd$' ./internal/cli/`
Expected: FAIL, unknown subcommand `check`; the scaffold lacks `plugins.go`

- [ ] **Step 3: Implement**

**`plugins.go.tmpl`** is the exact text of `pluginsSkeleton`. Its header comment
is the spec §3 comment. `func plugins() []collage.Plugin { return []collage.Plugin{} }`
is gofmt-formatted, with the list on its own lines. Make Task 3's const and this
file one source of truth: the const reads the embedded template.
`scaffoldFS.ReadFile("scaffold/common/plugins.go.tmpl")` is available in package
`cli`, and the file has no `@@MODULE@@` token. Replace Task 3's literal const
with a `var pluginsSkeleton = mustReadSkeleton()`.

**`pluginCheck(dir, release, run)`:**
1. Run `plugincontract.Check(dir, "")`. On success print `<name>: meets the contract`.
2. **`release`:** run `git -C dir tag --points-at HEAD`. With no `v*` tag, fail with `no release tag at HEAD; add rule 5 with git tag vX.Y.Z`. The highest `v*` tag must not be a pseudo-version.
3. **`run`:**
   - Make `dir/collage-plugin-check-<random>` with `os.MkdirTemp(dir, "collage-plugin-check-")`, and remove it afterwards with `defer os.RemoveAll`.
   - Write `main.go` into it:

     ```go
     package main

     import (
     	"encoding/json"
     	"fmt"
     	"os"

     	"github.com/Elagoht/collage/pkg/collage"
     	plugin "<module path from dir/go.mod>"
     	// one import per required plugin, see below
     )

     func main() {
     	p := <Constructor.Call("plugin")>
     	if p.Name() != "<manifest name>" {
     		fmt.Fprintf(os.Stderr, "Name() is %q, collage.json says %q\n", p.Name(), "<manifest name>")
     		os.Exit(1)
     	}
     	app, err := collage.New(&collage.Config{
     		Plugins:      []collage.Plugin{<required…>, p},
     		PluginConfig: map[string]json.RawMessage{"<name>": json.RawMessage("{}")},
     	})
     	if err == nil {
     		err = app.Start()
     	}
     	if err != nil {
     		fmt.Fprintln(os.Stderr, err)
     		os.Exit(1)
     	}
     	fmt.Println("<name>: builds and starts")
     }
     ```

   - **Required plugins:** for each name in `Manifest.Requires`, find its module among `go list -m -json all` (run in `dir`). Match it by `strings.EqualFold(path, ConventionalPath(name))`. Import it as `req0`, `req1`, … and construct it with `ReadConstructor(thatModule.Dir).Call("reqN")`. When a required module is not in the plugin's go.mod, print `<name>: <required> is not among its requirements; checked without it` and leave it out.
   - Run `go run ./<tempdir-name>` in `dir`. Pass its stdout through. On failure, return its stderr. Verify that a directory name with this prefix is accepted by `go run`; if not, change the prefix and say so in the report.
4. **Command line:** `check` takes `-release`, `-run` and one optional positional dir, default `.`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/cli/...`
Expected: PASS, the whole cli package including the existing new/add tests

- [ ] **Step 5: Commit**

```bash
git add internal/cli/plugincheck.go internal/cli/plugincheck_test.go internal/cli/plugin.go internal/cli/plugin_test.go internal/cli/pluginsgo.go internal/cli/scaffold/common/plugins.go.tmpl internal/cli/scaffold/common/main.go.tmpl internal/cli/new_test.go internal/cli/fakeproxy_test.go
git commit -m "feat(cli): plugin check, and new projects start with plugins.go"
```

---

### Task 8: docs in the repo, CHANGELOG, whole-suite run

**Files:**
- Modify: `CHANGELOG.md` (a `## v0.58.0` section at the top)
- Modify: `internal/cli/scaffold/minimal/README.md.tmpl` and `internal/cli/scaffold/demo/README.md.tmpl`, in the paragraph that mentions `plugins-config.json` (lines ~35 and ~122)
- Modify: `README.md` at the repo root, if it lists CLI commands (grep for `collage add`)

- [ ] **Step 1: CHANGELOG**

```markdown
## v0.58.0

### Added

- **`collage plugin add <owner/name>`.** It fetches `github.com/<owner>/collage-<name>`
  with `go get`, checks that it is a collage plugin, adds it to `plugins.go`
  (with its import, in the place its manifest's `order` asks for), and adds an
  empty section `"<owner/name>": {}` to `plugins-config.json`. A full module
  path (`gitlab.com/x/y`) is used as written, and `@version` pins a release.
  Plugins it `requires` are added first. Any failure leaves `go.mod`, `go.sum`,
  `plugins.go` and `plugins-config.json` as they were.
- **`collage plugin del <owner/name>`.** It removes the plugin from `plugins.go`
  and `plugins-config.json`, prints the settings its section held, and runs
  `go mod tidy`. It refuses while another plugin requires it.
- **`collage plugin check [-release] [-run] [dir]`.** It checks a plugin
  repository against the contract `add` enforces:
  - a `collage.json` naming it `owner/name`;
  - a `New` that can be called with nothing configured;
  - with `-release`, a release tag;
  - with `-run`, a throwaway application built with it.
- **The manifest gains `requires`, `order` and `setup`.**
- **`collage.FindPlugin[T](host)`.** It finds another of the application's
  plugins by type, from `Configure` or `Init`.
- **New projects get `plugins.go`,** and `main.go` passes `Plugins: plugins()`.
  An existing project gets the file from its first `collage plugin add`. Pass
  `plugins()` to `collage.New` once, as the command says.
```

- [ ] **Step 2: Scaffold READMEs**

In both templates, replace the sentence that tells the user to add plugins by
hand with:

```markdown
Add a plugin with `collage plugin add elagoht/sitemap`: it is fetched, added to
`plugins.go`, and given an empty section in `plugins-config.json`, where its
settings go. `collage plugin del elagoht/sitemap` takes it out again.
```

- [ ] **Step 3: Whole suite, vet, gofmt, `any` check**

Run:
- `go test -count=1 ./... 2>&1 | tail -30`
- `go vet ./...`
- `gofmt -l .`
- `git diff main --stat`
- `git diff main -- '*.go' | grep -nE '^\+.*(\bany\b|interface\{ ?\})' | grep -v '// any:'`

Expected: all packages ok, no vet output, no gofmt output, and no new `any`.

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md internal/cli/scaffold/minimal/README.md.tmpl internal/cli/scaffold/demo/README.md.tmpl
git commit -m "docs: collage plugin in the CHANGELOG and the scaffolded READMEs"
```

---

### Task 9: plugin repos follow-up (after collage v0.58.0 is tagged)

The controller runs this during the release, not on the collage branch. Each
item is TDD'd in its own repo with that repo's existing test style. Each repo
drops nothing (no replace to remove), moves to collage v0.58.0 via `go get`,
bumps `Version()` and the version-pinning test, and adds a README Changes entry.

- **collage-websocket (minor):**
  - `New(nil)` works. In `Configure` (implement `collage.Configurer` if it
    doesn't), a nil `lv` is replaced by `collage.FindPlugin[*live.Plugin](host)`,
    and `UseTransport` is called there. No live found is an error,
    `websocket: no collage-live plugin to carry; add elagoht/live`.
  - Manifest: `"requires": ["elagoht/live"], "order": -1`.
  - Test: `New(nil)` listed after `live.New()` in `Config.Plugins` serves
    `/_live` over ws. This is the existing ws round-trip test with `New(nil)`.
- **collage-feed (patch):**
  - With zero feeds, `Init` logs one Warn: `feed: no feeds; pass them to feed.New in plugins.go`.
  - Manifest `setup`: "Feeds are Go values: give them to `feed.New(...)` in `plugins.go`, each with its `Items` function."
  - Test: a captured logger sees the Warn once.
- **collage-health, -fail2ban, -compress, -devtoolbar (patch):** manifest `order`
  of −30, −25, −20 and 100. No code change, no test.
- **The seven drifted manifests** (accesslog, analytics, health, oauth,
  opti-image, secure, uploads) are regenerated with `schemagen -manifests`. Then
  the CLI's `check -run` is run on all 44 plugins; any failure gets a patch
  release with a test.

### Task 10: extension and CI (release)

- **Extension:**
  - `schemas/collage-plugin-manifest.schema.json` gains `requires`
    (array of the name pattern), `order` (integer) and `setup` (string).
  - `tools/schemagen` keeps those three fields when it rewrites a manifest.
    Write a test that regenerates a manifest containing them and finds them
    intact.
  - Release 0.17.0.
- **collage CI** (`.github/workflows/ci.yml`, plugins job): after `go test ./...`,
  add
  `if [ -f collage.json ]; then go run ../collage/cmd/collage plugin check -run .; fi`.
  The guard skips collage-paginate, which is a library.
- **Docs site (EN and TR):**
  - the CLI page gets the three commands;
  - a new page `plugin-authors` (the URL in `plugincontract.AuthorsURL`) carries the contract;
  - the plugins page leads with `collage plugin add`.

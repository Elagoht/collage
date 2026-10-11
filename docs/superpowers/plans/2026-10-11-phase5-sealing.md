# Phase 5 Sealing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the phase 5 inventory into one breaking release of `pkg/collage` (v0.60.0) whose public API is what v1.0 will freeze, plus a CI gate that reports API changes.

**Architecture:** `pkg/collage` stops leaking `internal/` method sets: `App` becomes a wrapper struct, `Host`/`ConfigHost`/`Plugin` become aliases to interface literals, and engine-only methods on aliased internal types become package functions in their internal package. The inventory's row-by-row decisions (renames, merges, removals, unexports, shape changes) are applied area by area, each area a task with its own tests. Three guard tests (method-set allowlist, no exported function vars, doc-comment hygiene) keep the surface from regrowing.

**Tech Stack:** Go 1.26, standard library only (go.mod has zero third-party dependencies and keeps it that way; tests use `go/parser`, `go/types`, `go/importer`, `reflect`).

**Spec:** `docs/superpowers/specs/2026-10-11-phase5-sealing-design.md`. The inventory tables it makes normative: `docs/superpowers/specs/phase5/inventory-a-collage.md`, `inventory-b-routes.md`, `inventory-c-config-errors.md`. Read the spec before your task; read the table rows your task names.

## Global Constraints

- Work in the worktree `/Users/furkan/Desktop/collage/.worktrees/phase5` on branch `feat/phase5-sealing`. Never commit on main.
- Never use the type `any` or `interface{}` in new code. Existing generic constraints (`[T any]`) stay as they are.
- No new module dependency: `go.mod` keeps zero `require` lines besides none it has today.
- Every commit: `gofmt -l .` prints nothing, `go vet ./...` passes, and the commit message ends with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- Never `git add -A` or `git add .`: read `git status --short` and add files by name.
- A test that names a changed identifier is updated to the new name, never deleted to make the suite pass.
- Where the spec and an inventory table conflict, the spec wins; rows marked KEEP are not touched.
- Doc comments in `pkg/collage` name no `internal/` package and no `types.`, `core.`, `plugin.`, `render.`, `httpx.`, `build.` identifier (Task 11 enforces it; write new comments that way from Task 1 on).
- Scaffold changes are verified with `go test -count=1 ./internal/cli/...` (the test cache hides scaffold breakage).
- Final suite command for every task: `go test -race -count=1 ./...` from the worktree root, plus `go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`.

## Review Focus

1. A plugin written against today's API that uses only KEEP names (`Init(ctx context.Context, host collage.Host) error`, `host.Logger()`, `host.Use(...)`) must still compile after Task 3 — pinned by `TestHost_ExternalPluginCompiles` in Task 3.
2. A builder's map or slice shared between the value `Build()` returned and the builder — e.g. `WithPath` after `Build` adding a path to the registered page — must not happen for any container field, including ones added later — pinned by the reflection test `TestBuild_ReturnsIndependentCopy` in Task 9.
3. An `ActionResult` that sets only `Status` (from `collage.Status(401)`) or only `Header` keeps answering as today; only two or more of `Location`, `Fragment`, `Page`, `Body` is refused — pinned by `TestActionResult_SingleOrNoBodyUnchanged` in Task 10.
4. `Cache: collage.CacheConfig{Type: "disk"}` (an untyped string literal, as every scaffolded `main.go` writes) still compiles after `Type` becomes `CacheType` — pinned by `TestCacheType_UntypedLiteral` in Task 8 and by the scaffold compile test.
5. `collage inspect`'s JSON must be byte-for-byte what it was before Inspection moved internal and App became a wrapper — pinned by the golden test in Task 1, re-run in Task 2.

---

### Task 1: Inspection leaves the public API

Spec: Design §6.2, §4 (CheckCommand, InspectCommand unexported). Table C rows `Inspection`, `Inspected*`, `CheckCommand`, `InspectCommand` (override: ASK → removed from `pkg/collage`).

**Files:**
- Modify: `pkg/collage/command.go` (DispatchCommands; the two consts), `pkg/collage/collage.go` or wherever `Inspection`/`Inspected*` aliases live (grep below)
- Move tests: `pkg/collage/inspect_test.go` → `internal/core/inspect_public_test.go` is NOT allowed (it builds through `collage.New`); instead keep it in `pkg/collage` as an internal-package test `pkg/collage/inspect_internal_test.go` (`package collage`)
- Create: `pkg/collage/testdata/inspect.golden.json`

**Interfaces:**
- Consumes: nothing.
- Produces: `pkg/collage` has no `Inspection`, `Inspected*`, `CheckCommand`, `InspectCommand`. Unexported consts `checkCommand = "collage-check"` and `inspectCommand = "collage-inspect"` (keep today's string values exactly). `DispatchCommands(ctx, app, args)` unchanged in signature and output.

- [ ] **Step 1: Create the worktree**

```bash
cd /Users/furkan/Desktop/collage
git worktree add -q .worktrees/phase5 -b feat/phase5-sealing
cd .worktrees/phase5
grep -rn "Inspection\|Inspected[A-Z]\|CheckCommand\|InspectCommand" pkg/collage --include='*.go' | grep -v _test
```

Expected: the alias lines and the DispatchCommands switch. Note the string values of the two consts.

- [ ] **Step 2: Write the golden test (passes today — it is the safety net)**

In `pkg/collage/inspect_internal_test.go` (`package collage`), build the same app `inspectedApp` builds in `inspect_test.go` (copy that helper), run the inspect command through `DispatchCommands` with stdout captured, and compare with the golden file. If DispatchCommands writes to `os.Stdout`, capture with a pipe:

```go
package collage

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata golden files")

func captureStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	was := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); done <- b }()
	fn()
	w.Close()
	os.Stdout = was
	return <-done
}

func TestInspect_GoldenJSON(t *testing.T) {
	app := inspectedAppInternal(t)
	out := captureStdout(t, func() {
		if _, err := DispatchCommands(context.Background(), app, []string{inspectCommandName()}); err != nil {
			t.Fatal(err)
		}
	})
	if *updateGolden {
		if err := os.WriteFile("testdata/inspect.golden.json", out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/inspect.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, want) {
		t.Errorf("collage inspect output changed:\n%s", out)
	}
}
```

`inspectCommandName()` is a test helper returning today's `InspectCommand` value; in Step 4 it switches to the unexported const. Check DispatchCommands' real return values and output destination first (`sed -n 60,120p pkg/collage/command.go`) and adapt the call — the point is: run the real command, capture its exact bytes. Any timing or path that varies between runs (a temp dir, a duration) must be fixed in the test app, not filtered from the output.

- [ ] **Step 3: Generate the golden file and see it pass**

```bash
mkdir -p pkg/collage/testdata
go test ./pkg/collage/ -run TestInspect_GoldenJSON -update -count=1
go test ./pkg/collage/ -run TestInspect_GoldenJSON -count=1 -v
```

Expected: PASS twice; `git diff --stat` shows the new golden file. Read it: it must be real inspect JSON with a `version` field.

- [ ] **Step 4: Remove the public Inspection surface**

- Delete the `Inspection` and every `Inspected*` alias from `pkg/collage`.
- Rename `CheckCommand` → `checkCommand`, `InspectCommand` → `inspectCommand` (values unchanged). Update `DispatchCommands`.
- `DispatchCommands` calls `app.Inspect()`; until Task 2, App is still an alias, so this compiles unchanged.
- Move the assertions of `pkg/collage/inspect_test.go` that call `app.Inspect()` into `inspect_internal_test.go` (they may call `app.Inspect()` there, since App is still `core.App`; Task 2 changes them to `app.c.Inspect()`). Delete `inspect_test.go` only once every assertion lives in the new file.
- `internal/cli` hard-codes the command strings; confirm with `grep -rn "collage-inspect\|collage-check" internal/cli | head` that nothing imported the consts.

- [ ] **Step 5: Run and commit**

```bash
gofmt -l . ; go vet ./... && go test -race -count=1 ./... 2>&1 | tail -15
git status --short
git add pkg/collage/command.go pkg/collage/inspect_internal_test.go pkg/collage/testdata/inspect.golden.json <the alias file> 
git rm pkg/collage/inspect_test.go
git commit -m "refactor!: Inspection is no longer public API; collage inspect's JSON is the contract

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

Expected: all packages ok.

---

### Task 2: App becomes a defined type; collage.Build replaces NewBuilder

Spec: Design §1, §5 (`collage.Build`, defined build types, `SkipRecord.Name`). Table A row `App`; table C rows `Builder`, `NewBuilder`, `BuildOptions`, `BuildReport`, `SkipRecord`, `WarningRecord`, `ErrNilRenderer`, `ErrInvalidOutDir`.

**Files:**
- Create: `pkg/collage/app.go` (the wrapper and its forwarding methods)
- Modify: `pkg/collage/collage.go` (remove `type App = core.App`, `New`), `pkg/collage/build.go`, `pkg/collage/command.go`, `internal/build/*.go` (record field rename `Page` → `Name`), `internal/cli/scaffold/common/main.go.tmpl`, every `pkg/collage` test that calls a hidden method
- Test: `pkg/collage/app_test.go`

**Interfaces:**
- Consumes: Task 1's unexported `inspectCommand`.
- Produces:
  - `type App struct{ c *core.App }` with exactly the forwarded methods listed in spec §1, same signatures as core's, types spelled with `pkg/collage` names.
  - `func New(cfg *Config) (*App, error)`.
  - `func Build(ctx context.Context, app *App, opts BuildOptions) (*BuildReport, error)`; `var ErrNilApp`, `var ErrEmptyOutDir`.
  - Defined structs `BuildOptions{OutDir string; Locales []string; Clean bool; Concurrency int; AllowDegraded bool}`, `BuildReport`, `SkipRecord{Name, Locale, Reason string; Err error}` (keep every other field the internal type has, renamed only `Page` → `Name`), `WarningRecord` (same rename) — copy field lists from `internal/build` exactly.
  - `NewBuilder`, `Builder`, `ErrNilRenderer`, `ErrInvalidOutDir` no longer exist.

- [ ] **Step 1: Write the failing tests**

`pkg/collage/app_test.go` (`package collage_test`):

```go
package collage_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

// The methods App exposes are exactly the spec's list: nothing internal/build
// alone needs is reachable from a user's *App.
func TestApp_MethodSet(t *testing.T) {
	want := []string{
		"ActionURL", "Actions", "BaseURL", "BuildID", "Check", "Commands", "DevMode",
		"Documents", "FragmentURL", "Handle", "Handler", "InvalidateTags", "InvalidateTagsN",
		"ListenAndServe", "Locales", "Logger", "Mount", "OriginFor", "Page", "PageURLs", "Pages",
		"Register", "RegisterAction", "RegisterCommand", "RegisterDocument", "RegisterErrorPage",
		"RegisterNotFoundPage", "RegisterPage", "RegisterPlugin", "RenderDocumentPath",
		"RenderFragment", "RenderPath", "ServeStatus", "Shutdown", "Start", "URL", "Use",
	}
	typ := reflect.TypeOf((*collage.App)(nil))
	var got []string
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("App methods =\n%v\nwant\n%v", got, want)
	}
}

func TestBuild_NilAppAndEmptyOutDir(t *testing.T) {
	if _, err := collage.Build(context.Background(), nil, collage.BuildOptions{OutDir: t.TempDir()}); !errors.Is(err, collage.ErrNilApp) {
		t.Errorf("nil app: %v", err)
	}
	app, err := collage.New(&collage.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collage.Build(context.Background(), app, collage.BuildOptions{}); !errors.Is(err, collage.ErrEmptyOutDir) {
		t.Errorf("empty OutDir: %v", err)
	}
}
```

If `collage.New(&collage.Config{})` needs templates to succeed, build the app the way the nearest existing build test does (`grep -ln "NewBuilder" pkg/collage/*_test.go`) and reuse its helper.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./pkg/collage/ -run 'TestApp_MethodSet|TestBuild_NilApp' -count=1`
Expected: compile failure (`collage.Build`, `ErrNilApp`, `ErrEmptyOutDir` undefined).

- [ ] **Step 3: Write `pkg/collage/app.go`**

```go
package collage

import "github.com/Elagoht/collage/internal/core"

// App is a collage application: its pages, documents, actions, plugins and
// the server that answers them. New returns one.
type App struct{ c *core.App }

// New validates cfg, applies its defaults and returns the application. A nil
// cfg is an error, not a request for the defaults.
func New(cfg *Config) (*App, error) {
	if cfg == nil {
		return nil, errNilConfig
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	c, err := core.New(toCoreConfig(cfg))
	if err != nil {
		return nil, err
	}
	return &App{c: c}, nil
}
```

Until Task 7/8 rename them, call the existing `ErrNilConfig`, `cfg.ApplyDefaults()`, `cfg.Validate()`; the snippet shows the end state. Then one forwarding method per name in the spec list, each with a real doc comment (one or two sentences: what it does, when to call it — take the substance from the core method's comment, without naming internal packages). Example of the shape:

```go
// Handler returns the application's http.Handler, starting it (plugins'
// Init included) on first use.
func (a *App) Handler() http.Handler { return a.c.Handler() }

// RenderPath renders the page registered at path for locale, bypassing the
// render cache, and overlays params onto the path parameters the router finds.
func (a *App) RenderPath(ctx context.Context, path, locale string, params map[string]string) (*Result, error) {
	return a.c.RenderPath(ctx, path, locale, params)
}
```

Delete `type App = core.App` and the old `New` from `collage.go`. Fix `DispatchCommands` and every other `pkg/collage` function that received `*App` and called a hidden method to use `app.c`.

- [ ] **Step 4: Replace NewBuilder with Build and define the build types**

In `pkg/collage/build.go`:

```go
// ErrNilApp is returned by Build for a nil *App.
var ErrNilApp = errors.New("collage: nil app")

// ErrEmptyOutDir is returned by Build when BuildOptions.OutDir is empty.
var ErrEmptyOutDir = build.ErrInvalidOutDir // rename the internal sentinel's message to "collage: empty output directory"

// Build renders app's static pages and documents and copies its mounts into
// opts.OutDir, and reports what it wrote, skipped and warned about.
func Build(ctx context.Context, app *App, opts BuildOptions) (*BuildReport, error) {
	if app == nil {
		return nil, ErrNilApp
	}
	b, err := build.New(app.c, opts.internal())
	if err != nil {
		return nil, err
	}
	r, err := b.Build(ctx)
	return reportFrom(r), err
}
```

Check `internal/build`'s real `New`/`Build` signatures (`grep -n "^func New\|^func (b \*Builder) Build" internal/build/builder.go`) and adapt: if `Build` takes opts instead of `New`, pass them there. Define `BuildOptions`, `BuildReport`, `SkipRecord`, `WarningRecord` as structs in `pkg/collage/build.go` with the internal types' exported fields (record field `Page` named `Name`), plus unexported converters `func (o BuildOptions) internal() build.Options` and `func reportFrom(*build.Report) *BuildReport` written field by field. `reportFrom(nil)` returns nil. Rename the internal records' `Page` field to `Name` too (`internal/build`), so the converter stays a plain copy. Delete `NewBuilder`, the `Builder` alias, `ErrNilRenderer`, `ErrInvalidOutDir`.

- [ ] **Step 5: Update callers**

```bash
grep -rln "NewBuilder\|ErrNilRenderer\|ErrInvalidOutDir\|\.Build(ctx\|SkipRecord{.*Page\|\.Skipped\[.*\]\.Page\|Warnings\[.*\]\.Page" --include='*.go' --include='*.tmpl' pkg internal | sort
```

Update each: the scaffold's `main.go.tmpl` becomes `report, err := collage.Build(ctx, app, collage.BuildOptions{...})`; tests that called hidden methods (`app.Mounts()`, `app.RenderNotFound`, `app.Inspect()`) move to `package collage` test files and call `app.c.X`. Re-point `inspect_internal_test.go` at `app.c.Inspect()`.

- [ ] **Step 6: Run and commit**

```bash
gofmt -l . ; go vet ./... && go test -race -count=1 ./... 2>&1 | tail -15
go test -count=1 ./internal/cli/... ./pkg/collage/ -run 'Golden|Scaffold|App_MethodSet|Build_' -v 2>&1 | grep -E "^(--- |ok|FAIL)"
git status --short   # add each changed file by name
git commit -m "refactor!: App is a defined type; collage.Build replaces NewBuilder

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

Expected: everything passes, the inspect golden test included (Review Focus 5).

---

### Task 3: Host, ConfigHost and Plugin as interface-literal aliases; Origins folds in

Spec: Design §2. Table A rows `Host`, `Plugin`, `Origins`; Host methods section.

**Files:**
- Modify: `internal/plugin/plugin.go` (Host, Plugin), `internal/plugin/configure.go` (ConfigHost), `internal/plugin/origin.go` (Origins goes), `internal/core/host.go` (Dynamic → MultiOrigin; drop RegisterPage, FragmentURL from hostView), `pkg/collage/collage.go` (aliases), every internal caller of `plugin.Origins` (`grep -rn "Origins)" internal`)
- Test: `pkg/collage/host_test.go`

**Interfaces:**
- Consumes: Task 2's `App`.
- Produces: `collage.Host` method set = DevMode, BaseURL, OriginFor, MultiOrigin, Locales, BuildID, Logger, RegisterCommand, RegisterDocument, Mount, Handle, Use, Pages, Page, PageURLs, URL, RenderFragment, ServeStatus, InvalidateTags + embedded ConfigReader. `collage.ConfigHost` gains `OriginFor(ctx context.Context, host string) string` and `MultiOrigin() bool`. `collage.Origins` gone.

- [ ] **Step 1: Write the failing tests**

`pkg/collage/host_test.go` (`package collage_test`):

```go
package collage_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

// A wrapper that embeds a Host (the pattern plugin tests use for spies) still
// answers origin questions: no type assertion is involved any more.
type spyHost struct{ collage.Host }

type originPlugin struct {
	multi  bool
	origin string
}

func (p *originPlugin) Name() string                   { return "test/origin" }
func (p *originPlugin) Version() string                { return "0" }
func (p *originPlugin) Shutdown(context.Context) error { return nil }
func (p *originPlugin) Init(ctx context.Context, host collage.Host) error {
	var h collage.Host = spyHost{host}
	p.multi = h.MultiOrigin()
	p.origin = h.OriginFor(ctx, "example.com")
	return nil
}

func TestHost_OriginThroughWrapper(t *testing.T) {
	p := &originPlugin{}
	app, err := collage.New(&collage.Config{BaseURL: "https://example.com", Plugins: []collage.Plugin{p}})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	if p.origin != "https://example.com" || p.multi {
		t.Errorf("origin %q multi %v", p.origin, p.multi)
	}
}

// Review Focus 1: a plugin using only names that stay compiles against Host.
type keepPlugin struct{}

func (keepPlugin) Name() string                   { return "test/keep" }
func (keepPlugin) Version() string                { return "0" }
func (keepPlugin) Shutdown(context.Context) error { return nil }
func (keepPlugin) Init(_ context.Context, host collage.Host) error {
	host.Logger().Info("hi")
	return host.Use(nil)
}

func TestHost_ExternalPluginCompiles(t *testing.T) {
	var _ collage.Plugin = keepPlugin{}
}

func TestHost_MethodSet(t *testing.T) {
	want := []string{"BaseURL", "BuildID", "DevMode", "Handle", "InvalidateTags", "Locales", "Logger",
		"Mount", "MultiOrigin", "OriginFor", "Page", "PageURLs", "Pages", "RegisterCommand",
		"RegisterDocument", "RenderFragment", "ServeStatus", "URL", "Use"}
	typ := reflect.TypeOf((*collage.Host)(nil)).Elem()
	var got []string
	for i := range typ.NumMethod() {
		if m := typ.Method(i); m.IsExported() {
			got = append(got, m.Name)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("Host methods =\n%v\nwant\n%v", got, want)
	}
}
```

Adjust `collage.New` arguments to whatever the smallest valid config is in this package's tests (templates may be required); `host.Use(nil)` may be refused — if so, call `host.Use(func(h http.Handler) http.Handler { return h })`.

- [ ] **Step 2: See them fail**

Run: `go test ./pkg/collage/ -run 'TestHost_' -count=1`
Expected: compile errors (`MultiOrigin` undefined) and/or a method-set mismatch.

- [ ] **Step 3: Implement**

- In `internal/plugin`, add `OriginFor` and `MultiOrigin` to `Host` and `ConfigHost`; delete `RegisterPage` and `FragmentURL` from `Host`; delete `Origins`. Rename `hostView.Dynamic`/`configHostView.Dynamic` → `MultiOrigin`; delete `hostView.RegisterPage`/`FragmentURL` if nothing internal calls them through the view.
- Replace every `h.(plugin.Origins)` assertion inside collage with a direct call.
- Make `Host`, `ConfigHost`, `Plugin` interface literals: in `internal/plugin`, `type Host = interface { ...methods with comments...; ConfigReader }`; in `pkg/collage`, `type Host = interface { ...the same methods spelled with pkg/collage names (*Page, PageURL, FragmentRequest, *FragmentRender, Command, MountOption)...; ConfigReader }`. The two literals must be identical types: every parameter type must be the same named type on both sides (pkg/collage's names are aliases of the internal ones, so this holds). Group the methods with comment headers: environment; contributing routes; wrapping requests; reading the site map; acting at request time.
- Delete the hand-written method lists in the Host/Plugin/App doc comments.

- [ ] **Step 4: See them pass, and the rest**

```bash
go test ./pkg/collage/ -run 'TestHost_' -count=1 -v | grep -E "^(--- |ok|FAIL)"
go doc ./pkg/collage Host | head -30    # methods and their comments are listed
gofmt -l . ; go vet ./... && go test -race -count=1 ./... 2>&1 | tail -15
```

- [ ] **Step 5: Commit**

```bash
git status --short
git commit -m "refactor!: Host answers OriginFor and MultiOrigin itself; Origins, Host.RegisterPage and Host.FragmentURL go

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Engine-only methods leave the aliased types

Spec: Design §3. Table B rows `Page`, `Fragment`; table A rows `Result`, `FragmentMetadata`, `Metadata`.

**Files:**
- Modify: `internal/types/page.go`, `internal/types/fragment.go`, `internal/types/datasource.go`, `internal/render/engine.go`, and every caller (`grep` below)
- Create: `pkg/collage/api_test.go` (the allowlist test; Tasks 8 and 11 add to it)

**Interfaces:**
- Consumes: nothing from earlier tasks beyond the build.
- Produces (all in their internal package, not reachable through `pkg/collage`):
  - `types.PageRoot(p *Page) *Fragment`, `types.PageGuards(p *Page) []GuardFunc`, `types.PageGuarded(p *Page) bool`, `types.PagePathFragments(p *Page) []*Fragment`, `types.PageContentSlotName(p *Page) string`
  - `types.LayoutFragment(p *Page) *Fragment`, `types.SetLayoutFragment(p *Page, f *Fragment)`; the field becomes `layoutFragment`
  - `types.Bind(f *Fragment, slot string, child *Fragment) error`, `types.FragmentDataSource(f *Fragment) DataSource`, `types.SetFragmentDataSource(f *Fragment, d DataSource)`, `types.EffectiveTimeout(f *Fragment, def time.Duration) time.Duration`, `types.FragmentSlot(f *Fragment, name string) (*SlotDefinition, bool)`, `types.FragmentSlotNames(f *Fragment) []string`
  - `render.HoistEnds(r *Result) map[string]int`; the field becomes `hoistEnds`
  - `Metadata.Fragments` is `[]types.FragmentReport`; `FragmentMetadata` and `Result.FragmentReports` gone.

- [ ] **Step 1: Write the failing allowlist test**

`pkg/collage/api_test.go` (`package collage_test`):

```go
package collage_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

func exportedMethods(typ reflect.Type) []string {
	var names []string
	for i := range typ.NumMethod() {
		names = append(names, typ.Method(i).Name)
	}
	return names
}

// An internal type reached through an alias publishes every exported method it
// has. These are the ones meant for users; a new one fails here until it is
// added on purpose.
func TestAliasedTypes_MethodAllowlist(t *testing.T) {
	for _, c := range []struct {
		typ  reflect.Type
		want []string
	}{
		{reflect.TypeOf((*collage.Page)(nil)), []string{"Locales", "PathFor", "Validate"}},
		{reflect.TypeOf((*collage.Fragment)(nil)), []string{"Validate"}},
		{reflect.TypeOf((*collage.Result)(nil)), []string{"Degraded"}},
	} {
		if got := exportedMethods(c.typ); !slices.Equal(got, c.want) {
			t.Errorf("%v methods = %v, want %v", c.typ, got, c.want)
		}
	}
}
```

Then add rows for `*collage.Document`, `*collage.Action` and `*collage.RenderContext` with whatever they export today (run the test once and copy the reported lists — those types are KEEP; the row pins them).

- [ ] **Step 2: See it fail**

Run: `go test ./pkg/collage/ -run TestAliasedTypes_MethodAllowlist -count=1`
Expected: FAIL listing Page's Root, Guards, …, Fragment's Bind, DataSource, …, Result's FragmentReports.

- [ ] **Step 3: Convert the methods**

For each method, add the function with the Produces name and body moved verbatim (`p.` → parameter), delete the method, then fix callers:

```bash
grep -rn "\.Root()\|\.Guards()\|\.Guarded()\|\.PathFragments()\|\.ContentSlotName()\|\.Bind(\|\.DataSource()\|\.SetDataSource(\|\.EffectiveTimeout(\|\.Slot(\|\.SlotNames()\|\.FragmentReports()\|\.HoistEnds\|\.LayoutFragment" --include='*.go' internal pkg | grep -v "_test.go" | wc -l
```

Fix every hit (tests too). `pkg/collage/fragment.go`'s `WithSlotFragment` calls `types.Bind(b.fragment, slotName, child)`. Replace `FragmentMetadata` by `FragmentReport` in `render.Metadata.Fragments` and its producers, delete `Result.FragmentReports()` and its alias row in `pkg/collage`.

- [ ] **Step 4: Pass, suite, commit**

```bash
gofmt -l . ; go vet ./... && go test -race -count=1 ./... 2>&1 | tail -15
git status --short
git commit -m "refactor!: engine-only methods of Page, Fragment and Result become internal functions

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Hooks, events and caches

Spec: Design §4 (hooks, RouteOf, Paths), §5 (Cache.Invalidate). Table A rows `PageResolvedHook`, `PageResolvedEvent`, `CacheWriteHook`, `CacheWriteEvent`, `RouteOf`, `CacheInvalidateEvent`, `Cache`, `TaggedCache`.

**Files:**
- Modify: `internal/plugin/hooks.go`, `internal/plugin/registry.go` (hook dispatch), `internal/core/*.go` (where the two hooks fire), `internal/cache/cache.go`, `internal/httpx` (RouteOf), `pkg/collage/collage.go`
- Test: `internal/cache/cache_test.go` or the nearest existing cache test file; `pkg/collage/api_test.go`

**Interfaces:**
- Produces: `cache.Cache` = Get, Set (no Invalidate); `cache.TaggedCache` = Cache + SetTagged + `Invalidate(ctx context.Context, tags []string) error`; `CacheInvalidateEvent` without `Paths`; no `RouteOf`, `PageResolvedHook`, `PageResolvedEvent`, `CacheWriteHook`, `CacheWriteEvent`.

- [ ] **Step 1: Failing test — a plain Cache needs no Invalidate**

In `pkg/collage/api_test.go` add:

```go
// A store that only gets and sets is a Cache; invalidating tags belongs to a
// TaggedCache, the only kind the framework invalidates.
type getSetCache struct{}

func (getSetCache) Get(context.Context, string) ([]byte, string, bool) { return nil, "", false }
func (getSetCache) Set(context.Context, string, []byte, time.Duration) (string, error) {
	return "", nil
}

func TestCache_GetSetIsEnough(t *testing.T) {
	var _ collage.Cache = getSetCache{}
}
```

(add `context` and `time` imports). Run: `go test ./pkg/collage/ -run TestCache_GetSetIsEnough` → compile error "missing method Invalidate".

- [ ] **Step 2: Implement**

- Move `Invalidate` from `Cache` to `TaggedCache` in `internal/cache`. Where core calls `Invalidate`, it already holds a `TaggedCache` (core/app.go near the line the table cites); fix any call on a plain `Cache`.
- Delete `CacheInvalidateEvent.Paths` and its population; keep `Entries`.
- Delete `RouteOf` from `pkg/collage` (and `httpx.RouteOf` if nothing else uses it). Callers use `RouteInfo(ctx).Kind/.Name`.
- Delete the four hook/event types and their dispatch code and registry fields. Grep: `grep -rn "PageResolved\|CacheWrite" --include='*.go' internal pkg`.

- [ ] **Step 3: Pass, suite, commit**

```bash
gofmt -l . ; go vet ./... && go test -race -count=1 ./... 2>&1 | tail -15
git commit -m "refactor!: unused PageResolved and CacheWrite hooks go; RouteOf merges into RouteInfo; Invalidate moves to TaggedCache

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Renames and shape changes in pages, documents, actions, data

Spec: Design §4 (renames), §5 (Redirect, SlotResolverFunc, WithSlot, Varied). Table B rows `JSON`, `JSONOf`, `NoContent`, `WithDependency` ×2, `WithSlot`, `Redirect`, `SlotResolverFunc`, `ErrMissingTTL`; table C rows `DataHandler`, `Varied`, `VariedContext`.

**Files:**
- Modify: `pkg/collage/action.go`, `pkg/collage/page.go`, `pkg/collage/document.go`, `pkg/collage/fragment.go`, `pkg/collage/data.go`, `pkg/collage/http.go`, `internal/types/page.go` (Redirect), `internal/types/fragment.go` (SlotResolverFunc, SlotRule), `internal/render` (calls the resolver), `internal/types` TTL sentinels
- Test: `pkg/collage/rename_test.go`

**Interfaces:**
- Produces:
  - `func JSON[T any](status int, v T) (*ActionResult, error)`; `func JSONBytes(status int, body []byte) *ActionResult`; `func Status(status int) *ActionResult`
  - `(*PageBuilder).WithDependencyTags(tags ...string) *PageBuilder`, `(*DocumentBuilder).WithDependencyTags(tags ...string) *DocumentBuilder`
  - `func LoadTagged[T any](fn func(context.Context, *RenderContext) (T, []string, error)) Data`
  - `func Varied(ctx context.Context, header string) (string, bool)`
  - `Redirect{From, To string; Status int}` (0 means 302); `WithPermanentRedirect` sets 301
  - `type SlotResolverFunc func(ctx context.Context, rc *RenderContext) ([]*Fragment, error)`
  - `type SlotRule int`; `const ( SlotRequired SlotRule = iota + 1; SlotSingle )`; `(*FragmentBuilder).WithSlot(name string, rules ...SlotRule) *FragmentBuilder` — no rule means optional and multiple; `SlotSingle` means at most one fragment
  - `ErrMissingTTL` gone; `Incremental(0)` and `Incremental(-1)` record `ErrInvalidTTL`

- [ ] **Step 1: Write the failing tests**

`pkg/collage/rename_test.go` (`package collage_test`):

```go
package collage_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

func TestJSON_ValueAndBytes(t *testing.T) {
	r, err := collage.JSON(http.StatusCreated, map[string]int{"n": 1})
	if err != nil || string(r.Body) != `{"n":1}` || r.Status != 201 || r.ContentType != "application/json" {
		t.Errorf("JSON = %+v, %v", r, err)
	}
	if b := collage.JSONBytes(200, []byte(`[]`)); string(b.Body) != "[]" {
		t.Errorf("JSONBytes = %+v", b)
	}
	if s := collage.Status(http.StatusUnauthorized); s.Status != 401 || s.Body != nil {
		t.Errorf("Status = %+v", s)
	}
}

func TestVaried_TakesContext(t *testing.T) {
	if v, ok := collage.Varied(context.Background(), "X-Test"); ok || v != "" {
		t.Errorf("Varied on a bare context = %q, %v", v, ok)
	}
}

func TestRedirect_StatusDefaultsAndPermanent(t *testing.T) {
	r := collage.Redirect{From: "/a", To: "/b"}
	if r.Status != 0 {
		t.Fatal("zero Status expected")
	}
	// The page builder's permanent helper writes 301 into Status.
	p := collage.NewPage("p").WithPermanentRedirect("/old", "/new")
	_ = p
}

func TestWithSlot_Rules(t *testing.T) {
	f := collage.NewFragment("f", "f.html").WithSlot("main", collage.SlotRequired, collage.SlotSingle).Build()
	_ = f
}

func TestIncremental_ZeroTTL(t *testing.T) {
	b := collage.NewPage("p").Incremental(0)
	b.Build()
	if !errors.Is(b.BuildErr(), collage.ErrInvalidTTL) {
		t.Errorf("Incremental(0): %v", b.BuildErr())
	}
}
```

Before running, check the real constructor names and signatures (`grep -n "^func New\(Page\|Fragment\|Document\|Action\)\|WithPermanentRedirect\|func (b \*PageBuilder) Incremental" pkg/collage/*.go`) and fix the test calls to match — keep the assertions. Strengthen `TestRedirect_StatusDefaultsAndPermanent` to read the built page's `Redirects[0].Status == 301`, and `TestWithSlot_Rules` to read the slot's `Required` and `AllowMultiple` (`true`, `false`) through `types`-free fields the public `Fragment` exposes (`f.Slots["main"]`).

- [ ] **Step 2: See them fail** — `go test ./pkg/collage/ -run 'TestJSON_|TestVaried_|TestRedirect_|TestWithSlot_|TestIncremental_' -count=1` → compile errors.

- [ ] **Step 3: Implement each row**

- `action.go`: rename `JSON` → `JSONBytes`, `JSONOf` → `JSON`, `NoContent` → `Status`.
- `page.go`/`document.go`: `WithDependency` → `WithDependencyTags`.
- `data.go`: `DataHandler` → `LoadTagged` (doc comment: "LoadTagged is Load for a handler that also returns the cache tags its data depends on").
- `http.go`: `Varied(ctx, header)` calls `httpx.VariedContext(ctx, header)`; delete `VariedContext`. Fix internal callers that passed `rc` to pass `rc.Request.Context()`.
- `types.Redirect`: drop `Permanent`, rename `StatusCode` → `Status`; `EffectiveStatus` returns `Status` or 302. `WithPermanentRedirect` (find it) sets `Status: 301`. Fix `internal/router` and build/redirect code (`grep -rn "StatusCode\|\.Permanent" --include='*.go' internal pkg`) — careful: `http.Response.StatusCode` and other unrelated `StatusCode`s are not this field.
- `types.SlotResolverFunc` gains `ctx`; the render engine passes its render context's `ctx`.
- `SlotRule` in `internal/types` with an alias and the two consts in `pkg/collage`; `WithSlot` computes `required := slices.Contains(rules, SlotRequired)`, `allowMultiple := !slices.Contains(rules, SlotSingle)` and keeps the rest of today's body. An unknown rule value records an error `fmt.Errorf("%w: rule %d", ErrInvalidSlotDefinition, r)`.
- Merge `ErrMissingTTL` into `ErrInvalidTTL`.
- Update every test and the scaffold: `grep -rn "JSONOf\|NoContent\|WithDependency(\|DataHandler\|VariedContext\|StatusCode:\|Permanent:\|WithSlot(\|ErrMissingTTL" --include='*.go' --include='*.tmpl' pkg internal`.

- [ ] **Step 4: Pass, suite, commit**

```bash
gofmt -l . ; go vet ./... && go test -race -count=1 ./... 2>&1 | tail -15 && go test -count=1 ./internal/cli/... 2>&1 | tail -3
git commit -m "refactor!: JSON takes a value, Status, WithDependencyTags, LoadTagged, Varied(ctx), Redirect.Status, slot rules, ctx for slot resolvers

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Sentinel errors

Spec: Design §4 (unexported, removed, merges, renames, errors.go). Table A rows `ErrEmptyCommandName`, `ErrEmptyPluginName`, `ErrNilConfig`, `ErrNilPage`, `ErrNilPlugin`, `ErrNoRootFragment`, `ErrUnsupportedCache`; table C rows `ErrDuplicateOutputPath` (merge happens in Task 10), `ErrEmptyCacheVersion`, `ErrEmptyLocaleDefault`, `ErrEmptyTemplateRoot`, `ErrInvalidBaseURL`, `ErrDictKeyNotString`, `ErrDictOddArgs`, `ErrNoMethods`, `ErrNoMountForAsset`, `ErrInvalidHandlerPrefix`.

**Files:**
- Modify: `pkg/collage/errors.go` and every `pkg/collage` file declaring an `Err*` var (move them all into `errors.go`, grouped under comment headers per area: configuration, registration, routing and URLs, rendering, actions, documents, static build, plugins); internal sentinels' messages where the spec renames them
- Test: `pkg/collage/api_test.go`

**Interfaces:**
- Produces: `ErrInvalidOrigin` (message `collage: invalid origin: want scheme://host[:port] with no path, query or fragment` or the internal sentinel's message reworded the same way — no "BaseURL"), `ErrNoActionMethods`, `ErrInvalidPrefix` covers handler prefixes, `ErrInvalidCacheType` covers the old unsupported-cache case. Gone from `pkg/collage`: `ErrNilConfig`, `ErrNilPage`, `ErrNilPlugin`, `ErrEmptyCommandName`, `ErrEmptyPluginName`, `ErrNoRootFragment`, `ErrEmptyCacheVersion`, `ErrDictKeyNotString`, `ErrDictOddArgs`, `ErrNoMountForAsset` (all unexported where defined, still returned), `ErrEmptyLocaleDefault`, `ErrEmptyTemplateRoot` (deleted), `ErrUnsupportedCache`, `ErrInvalidHandlerPrefix`, `ErrInvalidBaseURL`, `ErrNoMethods`.

- [ ] **Step 1: Failing test — every exported error lives in errors.go**

Add to `pkg/collage/api_test.go`:

```go
// Every exported sentinel is declared in errors.go, so the list a user reads is
// the whole list.
func TestErrors_AllInErrorsGo(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		for path, f := range p.Files {
			for _, d := range f.Decls {
				g, ok := d.(*ast.GenDecl)
				if !ok || g.Tok != token.VAR {
					continue
				}
				for _, s := range g.Specs {
					for _, n := range s.(*ast.ValueSpec).Names {
						if strings.HasPrefix(n.Name, "Err") && filepath.Base(path) != "errors.go" {
							t.Errorf("%s declared in %s", n.Name, filepath.Base(path))
						}
					}
				}
			}
		}
	}
}

func TestErrors_GoneFromAPI(t *testing.T) {
	src, err := os.ReadFile("errors.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ErrNilConfig", "ErrNilPage", "ErrNilPlugin", "ErrEmptyCommandName",
		"ErrEmptyPluginName", "ErrNoRootFragment", "ErrEmptyCacheVersion", "ErrDictKeyNotString",
		"ErrDictOddArgs", "ErrNoMountForAsset", "ErrEmptyLocaleDefault", "ErrEmptyTemplateRoot",
		"ErrUnsupportedCache", "ErrInvalidHandlerPrefix", "ErrInvalidBaseURL", "ErrNoMethods",
		"ErrMissingTTL", "ErrNilRenderer", "ErrInvalidOutDir"} {
		if regexp.MustCompile(`\b` + name + `\s*=`).Match(src) {
			t.Errorf("%s is still exported", name)
		}
	}
}

func TestErrInvalidOrigin_DoesNotBlameBaseURL(t *testing.T) {
	if strings.Contains(collage.ErrInvalidOrigin.Error(), "BaseURL") {
		t.Errorf("message = %q", collage.ErrInvalidOrigin.Error())
	}
}
```

`api_test.go` is `package collage_test` and runs with the package directory as working directory, so `"."` and `"errors.go"` resolve. Add the imports (`go/ast`, `go/parser`, `go/token`, `io/fs`, `os`, `path/filepath`, `regexp`, `strings`).

- [ ] **Step 2: See them fail** — `go test ./pkg/collage/ -run 'TestErrors_|TestErrInvalidOrigin' -count=1`.

- [ ] **Step 3: Implement** the Produces list: unexport (rename to lower case in `pkg/collage`, or drop the `pkg/collage` alias and keep the internal one), delete, merge (callers that `errors.Is(err, Old)` switch to the kept sentinel; internal code returns the kept one), rename, reword `ErrInvalidOrigin`'s internal message. Move every remaining `Err*` var declaration into `errors.go` under the area headers. Fix tests: `grep -rn "<each old name>" --include='*.go' pkg internal`.

- [ ] **Step 4: Pass, suite, commit**

```bash
gofmt -l . ; go vet ./... && go test -race -count=1 ./... 2>&1 | tail -15
git commit -m "refactor!: sentinel errors: nil and empty argument errors unexported, unreachable ones removed, duplicates merged, all in errors.go

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Config and function variables

Spec: Design §4 (`Config.Validate`, `ApplyDefaults`, `IsDevMode`), §5 (Config fields, function vars). Table C "Config fields" section and rows `Config.ApplyDefaults`, `Config.IsDevMode`, `Config.Validate`, `WithCacheControl`, `WithoutBuildCopy`.

**Files:**
- Modify: `pkg/collage/config.go`, `pkg/collage/collage.go` (`toCoreConfig`), `pkg/collage/mount.go`, `internal/core/app.go`, `internal/core/host.go`, the cache and locale code reading the removed fields, scaffold templates if they set any removed field
- Test: `pkg/collage/api_test.go`, `pkg/collage/config_test.go` (existing)

**Interfaces:**
- Produces: `Config` without `Template.DevMode`, `Locale.DisablePathLocale`, `Cache.MaxKeysPerTag`; `Security.DisableNoSniff bool`; `type CacheType string` with `const ( CacheMemory CacheType = "memory"; CacheDisk CacheType = "disk" )` and `CacheConfig.Type CacheType`; unexported `(*Config).applyDefaults()` and `(*Config).validate() error`; no `IsDevMode`; `func WithCacheControl(v string) MountOption`, `func WithoutBuildCopy() MountOption` (match the internal signatures).

- [ ] **Step 1: Failing tests**

Add to `pkg/collage/api_test.go`:

```go
// Review Focus 4: an untyped string literal still fills Cache.Type.
func TestCacheType_UntypedLiteral(t *testing.T) {
	c := collage.CacheConfig{Type: "disk"}
	if c.Type != collage.CacheDisk {
		t.Errorf("Type = %q", c.Type)
	}
}

// No exported variable holds a function: anyone could reassign it.
func TestNoExportedFuncVars(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]*ast.File, 0)
	for _, p := range pkgs {
		for _, f := range p.Files {
			files = append(files, f)
		}
	}
	conf := gotypes.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("github.com/Elagoht/collage/pkg/collage", fset, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range pkg.Scope().Names() {
		v, ok := pkg.Scope().Lookup(name).(*gotypes.Var)
		if !ok || !v.Exported() {
			continue
		}
		if _, isFunc := v.Type().Underlying().(*gotypes.Signature); isFunc {
			t.Errorf("exported var %s holds a function; declare a func", name)
		}
	}
}
```

Imports: `go/importer`, `gotypes "go/types"`. The source importer type-checks dependencies from source; it needs the module root reachable, which `go test` provides.

Update `pkg/collage/config_test.go`: tests calling `cfg.Validate()`/`ApplyDefaults()` from `package collage_test` move to a `package collage` file and call the unexported names, or assert through `collage.New`.

- [ ] **Step 2: See them fail** — `go test ./pkg/collage/ -run 'TestCacheType_|TestNoExportedFuncVars' -count=1` → compile error, then the two vars reported.

- [ ] **Step 3: Implement** the Produces list. `toCoreConfig` stops copying the removed fields; `core` reads `cfg.DevMode` alone (delete the `|| cfg.Template.DevMode` disjunctions in `internal/core/app.go` and `host.go`); the tag tracker's per-tag cap becomes the cache's `MaxEntries` (remove the 10000 default for MaxKeysPerTag); `DisablePathLocale` code paths go (a single supported locale already behaves the same — keep that behaviour covered by the existing locale tests); `NoSniff` logic inverts to `DisableNoSniff`. Any other exported function var the test reports becomes a func in the same step. Update the docs comments that mention removed fields.

- [ ] **Step 4: Pass, suite (scaffold included), commit**

```bash
gofmt -l . ; go vet ./... && go test -race -count=1 ./... 2>&1 | tail -15 && go test -count=1 ./internal/cli/... 2>&1 | tail -3
git commit -m "refactor!: Config sheds Template.DevMode, DisablePathLocale and MaxKeysPerTag; DisableNoSniff; typed CacheType; Validate and ApplyDefaults unexported; mount options are funcs

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Builders return copies; ActionBuilder.BuildErr; WithPath refuses repeats

Spec: Design §6.3, §6.6.

**Files:**
- Create: `internal/types/clone.go` (`ClonePage`, `CloneFragment`, `CloneDocument`, `CloneAction`)
- Modify: `pkg/collage/page.go`, `fragment.go`, `document.go`, `action.go`; `internal/types/builderr.go` (`Action` gets `setBuildErr`); registration of actions honours a recorded build error as pages do; `pkg/collage/errors.go` (new sentinels)
- Test: `pkg/collage/builder_copy_test.go`, `internal/types/clone_test.go`

**Interfaces:**
- Produces: `types.ClonePage(*Page) *Page` (and the three siblings) — struct copied, every slice and map field cloned, `SlotDefinition` values under `Fragment.Slots` copied with their `Fill` slices cloned; `(*ActionBuilder).BuildErr() error`; `var ErrDuplicateLocalePath`; `WithPath(locale, …)` with `locale == ""` records `ErrInvalidLocale` if that sentinel exists (`grep -n "ErrInvalidLocale\|ErrUnsupportedLocale" internal pkg -r`), otherwise a new `ErrEmptyLocale`.

- [ ] **Step 1: Failing tests**

`internal/types/clone_test.go`:

```go
package types

import (
	"reflect"
	"testing"
)

// Review Focus 2: after a clone no slice or map is shared, for every field the
// types have now or gain later.
func assertNoSharedContainers(t *testing.T, a, b reflect.Value, path string) {
	t.Helper()
	for i := range a.NumField() {
		fa, fb := a.Field(i), b.Field(i)
		name := path + "." + a.Type().Field(i).Name
		switch fa.Kind() {
		case reflect.Slice, reflect.Map:
			if !fa.IsNil() && fa.Len() > 0 && fa.UnsafePointer() == fb.UnsafePointer() {
				t.Errorf("%s is shared after clone", name)
			}
		}
	}
}

func TestClone_NoSharedContainers(t *testing.T) {
	p := &Page{Name: "p", Paths: map[string]string{"en": "/"}, LayoutChain: []*Fragment{{Name: "l"}},
		Redirects: []*Redirect{{From: "/a", To: "/b"}}, DependencyTags: []string{"t"}}
	assertNoSharedContainers(t, reflect.ValueOf(p).Elem(), reflect.ValueOf(ClonePage(p)).Elem(), "Page")

	f := &Fragment{Name: "f", Slots: map[string]*SlotDefinition{"s": {Name: "s", Fill: []*Fragment{{Name: "c"}}}}}
	c := CloneFragment(f)
	assertNoSharedContainers(t, reflect.ValueOf(f).Elem(), reflect.ValueOf(c).Elem(), "Fragment")
	if c.Slots["s"] == f.Slots["s"] {
		t.Error("SlotDefinition pointer shared")
	}
}
```

Fill every slice and map field of `Page`, `Fragment`, `Document`, `Action` with one element in the fixtures (read the struct definitions; the test is only as good as its fixture — a nil field is skipped). Add the Document and Action cases the same way.

`pkg/collage/builder_copy_test.go`:

```go
package collage_test

import (
	"errors"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

func TestBuild_ReturnsIndependentCopy(t *testing.T) {
	b := collage.NewPage("p").WithPath("en", "/a") // add WithContent(...) as the real builder needs
	p := b.Build()
	b.WithPath("tr", "/b")
	if _, ok := p.Paths["tr"]; ok {
		t.Error("WithPath after Build reached the built page")
	}
}

func TestBuild_TwiceRecordsOnce(t *testing.T) {
	b := collage.NewPage("p") // no content: Build records ErrMissingContent
	b.Build()
	b.Build()
	if n := countJoined(b.BuildErr()); n != 1 {
		t.Errorf("errors after two Builds = %d, want 1", n)
	}
}

func countJoined(err error) int {
	if err == nil {
		return 0
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return len(j.Unwrap())
	}
	return 1
}

func TestActionBuilder_BuildErr(t *testing.T) {
	b := collage.NewAction("a") // no handler
	b.Build()
	if b.BuildErr() == nil {
		t.Error("an action with no handler records no error")
	}
}

func TestWithPath_RepeatAndEmptyLocale(t *testing.T) {
	b := collage.NewPage("p").WithPath("en", "/a").WithPath("en", "/b").WithPath("", "/c")
	b.Build()
	if !errors.Is(b.BuildErr(), collage.ErrDuplicateLocalePath) {
		t.Errorf("repeat: %v", b.BuildErr())
	}
}
```

Fix constructor names/arguments against the real API first (`grep -n "^func New" pkg/collage/*.go`); keep the assertions. Add the same `TestBuild_ReturnsIndependentCopy` for fragment (`WithSlot` after `Build`), document (`WithPath` after `Build`) and action (`WithMethods` after `Build`), and the empty-locale assertion with the sentinel chosen above.

- [ ] **Step 2: See them fail** — `go test ./internal/types/ ./pkg/collage/ -run 'Clone|Build_|ActionBuilder_BuildErr|WithPath_' -count=1`.

- [ ] **Step 3: Implement**

- `clone.go`: one function per type, `c := *p` then `c.X = slices.Clone(p.X)` / `maps.Clone(p.X)` for every container field; `CloneFragment` copies each `*SlotDefinition` (`d := *s; d.Fill = slices.Clone(s.Fill)`).
- Each `Build()` records its errors only on the first call (a `built bool` on the builder, or dedupe by not appending the Build-time error twice), records them on the clone (`types.RecordBuildErr(clone, b.BuildErr())`) and returns the clone.
- `ActionBuilder` gets `errs []error`, `BuildErr()`, records a nil handler and a negative timeout (whatever `Action.Validate` checks at registration today) and `Build` records them on the action; registration refuses an action with a recorded build error, as it does pages (look at how `RegisterPage` reads `types.PageBuildErr` or similar).
- `WithPath` on page, document and action: a locale already in `Paths` records `fmt.Errorf("%w: %q on %s %q", ErrDuplicateLocalePath, locale, kind, name)`; `""` records the empty-locale sentinel. `AtRoot` keeps writing the root locale.

- [ ] **Step 4: Pass, suite, commit**

```bash
gofmt -l . ; go vet ./... && go test -race -count=1 ./... 2>&1 | tail -15
git commit -m "feat!: Build returns an independent copy; ActionBuilder.BuildErr; WithPath refuses a repeated or empty locale

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: Ambiguous ActionResult; output path collisions

Spec: Design §6.4, §6.5; table C row `ErrDuplicateOutputPath` (merge).

**Files:**
- Modify: `internal/httpx/action.go` (result dispatch near the `switch { case result.Location != "":` line), `internal/types/action.go` (new sentinel), `internal/build/document.go` (`dedupeDocumentTargets`), `internal/build/builder.go` (page targets shared with documents), `pkg/collage/errors.go`
- Test: `internal/httpx/action_test.go` (existing file — add to it), `internal/build/document_test.go` or the nearest build test

**Interfaces:**
- Produces: `var ErrAmbiguousActionResult` (in `internal/types`, aliased in `pkg/collage/errors.go`), message names the set fields: `collage: action result sets more than one of Location, Fragment, Page, Body: Page, Body`. `ErrDuplicateOutputPath` gone; documents colliding with a document or a page return `ErrOutputPathCollision` from the build.

- [ ] **Step 1: Failing tests**

In `internal/httpx/action_test.go` (it has `actionEnv`, `action`, `post`, `testPage`, `env.do`, `env.engine.pageCalls`):

```go
func TestActionResult_TwoBodiesRefused(t *testing.T) {
	page := testPage("new", "/posts/new", types.StrategyDynamic)
	both := action("both", "/posts/new", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return &types.ActionResult{Page: page, Body: []byte("raw-body")}, nil
		})
	env := actionEnv(t, []*types.Page{page}, []*types.Action{both})
	res := env.do(post("/posts/new", ""))

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
	if strings.Contains(res.Body.String(), "raw-body") {
		t.Errorf("the body was written: %q", res.Body.String())
	}
	if env.engine.pageCalls("new") != 0 {
		t.Errorf("the page was rendered %d times", env.engine.pageCalls("new"))
	}
}

// Review Focus 3: a result with one body, or none, answers as before.
func TestActionResult_SingleOrNoBodyUnchanged(t *testing.T) {
	deny := action("deny", "/deny", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return &types.ActionResult{Status: http.StatusUnauthorized}, nil
		})
	header := action("header", "/header", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) {
			return &types.ActionResult{Header: http.Header{"X-A": {"1"}}}, nil
		})
	env := actionEnv(t, nil, []*types.Action{deny, header})

	if res := env.do(post("/deny", "")); res.Code != http.StatusUnauthorized || res.Body.Len() != 0 {
		t.Errorf("Status-only result = %d %q", res.Code, res.Body.String())
	}
	res := env.do(post("/header", ""))
	if res.Code != http.StatusNoContent || res.Header().Get("X-A") != "1" {
		t.Errorf("Header-only result = %d, X-A %q", res.Code, res.Header().Get("X-A"))
	}
}
```

Before running, check what a header-only result answers today (run the test on the unchanged code first; if it is not 204, set the expectation to today's code — the test pins today's behaviour, it does not change it).

In `pkg/collage/errorevent_test.go`, using `eventRecorder`, `lockedBuffer` and `errorEventApp`:

```go
func TestActionResult_TwoBodiesReachTheErrorHook(t *testing.T) {
	rec := &eventRecorder{}
	app := errorEventApp(t, rec, &lockedBuffer{})
	page := collage.NewPage("p") // build a page over t/p.html the way this file's other tests do
	act := collage.NewAction("both").WithPath("en", "/both").WithMethods(http.MethodPost).
		WithHandler(func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			return &collage.ActionResult{Page: page.Build(), Body: []byte("x")}, nil
		}).Build()
	if err := app.Register(act); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/both", nil)
	app.Handler().ServeHTTP(httptest.NewRecorder(), req)

	events := rec.at("/both")
	if len(events) != 1 || !errors.Is(events[0].Err, collage.ErrAmbiguousActionResult) ||
		!strings.Contains(events[0].Err.Error(), "Page") || !strings.Contains(events[0].Err.Error(), "Body") {
		t.Errorf("events = %+v", events)
	}
}
```

Match the real builder method names (`WithHandler`, `WithMethods`, page construction, CSRF: disable it in this app's config if the POST is refused with 403 — `Security: collage.SecurityConfig{DisableCSRF: true}` in a copy of `errorEventApp`) and the `ErrorEvent` field holding the error (`grep -n "type ErrorEvent struct" -A12 internal/plugin/hooks.go`).

In `internal/build`'s tests (find the file that tests `dedupeDocumentTargets` or document builds: `grep -ln "ErrDuplicateOutputPath" internal/build/*_test.go`), change its expectation and add the cross-kind case:

```go
func TestBuild_DocumentCollisionStopsBuild(t *testing.T) {
	// Reuse the existing test's fake Renderer and two documents whose paths
	// resolve to one output file (e.g. "/feed.xml" and "/feed.xml/" or the
	// existing test's pair). Build must return an error wrapping
	// ErrOutputPathCollision and the report must hold no SkipRecord for it.
}
```

Write it as code on top of the existing collision test's fixture: the assertions are `errors.Is(err, ErrOutputPathCollision)` and `len(report.Skipped) == 0` for that document; then the same with one page and one document resolving to the same file.

- [ ] **Step 2: See them fail** — run the new tests by name.

- [ ] **Step 3: Implement**

- Before the result `switch` in `httpx`, count the set fields among `Location != ""`, `Fragment != nil`, `Page != nil`, `len(Body) > 0`; with two or more, report `fmt.Errorf("%w: %s", types.ErrAmbiguousActionResult, strings.Join(names, ", "))` through the same path an action handler's returned error takes (so it reaches the ErrorHook and the response is the 500 error page), and return.
- `dedupeDocumentTargets` returns an error instead of a skip; `Build` stops with it. Claim page targets in the same map documents check against (compute both sets of resolved output paths before rendering anything). Delete `ErrDuplicateOutputPath` everywhere.

- [ ] **Step 4: Pass, suite, commit**

```bash
gofmt -l . ; go vet ./... && go test -race -count=1 ./... 2>&1 | tail -15
git commit -m "feat!: an action result with two bodies is refused; colliding document outputs stop the build

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 11: Doc hygiene, AfterRenderEvent.Path, DEFINE leftovers

Spec: Design §7, §5 (AfterRenderEvent.Path), §2 doc grouping; inventory rule 11.

**Files:**
- Modify: `internal/plugin/hooks.go` (`AfterRenderEvent.Path string`), `internal/core/app.go` and `internal/httpx/action.go` (set it where the event is built), doc comments across `pkg/collage`
- Test: `pkg/collage/api_test.go`, an AfterRender test next to the existing ones (`grep -ln "AfterRenderEvent" pkg/collage/*_test.go internal/core/*_test.go`)

**Interfaces:**
- Produces: `AfterRenderEvent.Path` — the request path that was rendered (the same value `BeforeRenderEvent.Path` carries).

- [ ] **Step 1: Failing tests**

Add to `pkg/collage/api_test.go`:

```go
// Public doc comments name no internal package or identifier.
func TestDocComments_NoInternalNames(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	bad := regexp.MustCompile(`internal/|\b(types|core|plugin|render|httpx|build|router|cache|asset|observability)\.[A-Z]`)
	for _, p := range pkgs {
		for path, f := range p.Files {
			for _, cg := range f.Comments {
				if m := bad.FindString(cg.Text()); m != "" {
					t.Errorf("%s: %s: comment names %q", filepath.Base(path), fset.Position(cg.Pos()), m)
				}
			}
		}
	}
}
```

And an AfterRender test asserting `ev.Path == "/the/path"` for a rendered page.

- [ ] **Step 2: See them fail** — the hygiene test lists every offending comment.

- [ ] **Step 3: Fix** each reported comment by saying the same thing in public terms (e.g. "an alias for internal/core's App" → delete; "types.ErrInvalidTimeout" → "ErrInvalidTimeout"). Add and populate `AfterRenderEvent.Path`.

- [ ] **Step 4: Pass, suite, commit**

```bash
gofmt -l . ; go vet ./... && go test -race -count=1 ./... 2>&1 | tail -15
git commit -m "docs!: public doc comments name no internal identifiers; AfterRenderEvent.Path

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 12: The apidiff gate

Spec: Design §8.

**Files:**
- Modify: `.github/workflows/ci.yml` (new job `api`)
- Create: `.github/apidiff.sh`

**Interfaces:**
- Produces: a CI job `api` on every push and pull request; reports to the job summary; fails only when a `v1.*` tag exists and the change is incompatible against the latest one.

- [ ] **Step 1: Write the script**

`.github/apidiff.sh`:

```bash
#!/usr/bin/env bash
# Compares pkg/collage and pkg/collagetest with a released tag and writes the
# report to the job summary. Before any v1 tag it only reports; after one it
# fails on an incompatible change against the latest v1 tag.
set -euo pipefail
APIDIFF="go run golang.org/x/exp/cmd/apidiff@v0.0.0-20250106191152-7588d65b2ba8"

v1=$(git tag --list 'v1.*' --sort=-v:refname | head -n 1)
base=${v1:-$(git tag --list 'v*' --sort=-v:refname | head -n 1)}
summary=${GITHUB_STEP_SUMMARY:-/dev/stdout}
echo "## API against ${base}" >> "$summary"

git worktree add -q "$RUNNER_TEMP/base" "$base"
status=0
for pkg in pkg/collage pkg/collagetest; do
  [ -d "$RUNNER_TEMP/base/$pkg" ] || continue
  (cd "$RUNNER_TEMP/base" && $APIDIFF -w "$RUNNER_TEMP/old.export" "./$pkg")
  $APIDIFF -w "$RUNNER_TEMP/new.export" "./$pkg"
  report=$($APIDIFF -incompatible "$RUNNER_TEMP/old.export" "$RUNNER_TEMP/new.export" || true)
  full=$($APIDIFF "$RUNNER_TEMP/old.export" "$RUNNER_TEMP/new.export" || true)
  { echo "### $pkg"; echo '```'; echo "${full:-no changes}"; echo '```'; } >> "$summary"
  if [ -n "$v1" ] && [ -n "$report" ]; then
    echo "incompatible change in $pkg against $v1:"; echo "$report"; status=1
  fi
done
exit $status
```

Before committing, look up the newest `golang.org/x/exp` pseudo-version that is at least two weeks old (`GOFLAGS=-mod=mod go list -m -versions golang.org/x/exp` does not list pseudo-versions; use `go list -m -json golang.org/x/exp@latest` and pin its `Version`), and confirm `apidiff`'s flags with `go run golang.org/x/exp/cmd/apidiff@<version> -h` — adjust `-w` / `-incompatible` to what that version accepts. The script must run locally: `RUNNER_TEMP=$(mktemp -d) bash .github/apidiff.sh` from the worktree prints a report against the latest tag and exits 0.

- [ ] **Step 2: Add the job**

In `.github/workflows/ci.yml`, after `lint`:

```yaml
  # What changed in the public API against the last release. Before v1.0 it
  # only reports; from v1.0 on, an incompatible change fails here, and the way
  # to make one is a v2 module path.
  api:
    name: api
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'
          check-latest: true
          cache: true
      - name: apidiff
        run: bash .github/apidiff.sh
```

- [ ] **Step 3: Run locally and commit**

```bash
chmod +x .github/apidiff.sh
RUNNER_TEMP=$(mktemp -d) bash .github/apidiff.sh; echo "exit $?"
git add .github/apidiff.sh .github/workflows/ci.yml
git commit -m "ci: an api job reports pkg/collage's changes against the last release, and gates them from v1

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

Expected: exit 0 and a report listing this branch's breaking changes against v0.59.0 (it is report mode: no v1 tag).

---

### Task 13: Framework docs, scaffold, CHANGELOG, the plugin sweep list

Spec: "Plugins and other dependents", Release step 1.

**Files:**
- Modify: `docs/*.md` (framework docs), `CHANGELOG.md`, `pkg/collagetest` if it names a changed identifier, scaffold templates not yet updated
- Create: `docs/superpowers/specs/phase5/plugin-sweep.md`

- [ ] **Step 1: Find every old name in the framework's own docs and templates**

```bash
OLD='NewBuilder|ErrNilRenderer|ErrInvalidOutDir|Origins\b|RouteOf|JSONOf|NoContent|WithDependency\(|DataHandler|VariedContext|StatusCode:|Permanent:|ErrMissingTTL|PageResolved|CacheWrite|FragmentMetadata|IsDevMode|DisablePathLocale|MaxKeysPerTag|NoSniff|ApplyDefaults|\.Validate\(\)|Inspection|Inspected|CheckCommand|InspectCommand|ErrInvalidBaseURL|ErrNoMethods|ErrInvalidHandlerPrefix|ErrUnsupportedCache|ErrDuplicateOutputPath|LayoutFragment|SetDataSource|HoistEnds'
grep -rnE "$OLD" docs/*.md internal/cli/scaffold pkg/collagetest README.md | grep -v "docs/superpowers"
```

Update every hit to the new API. `.Validate()` hits are only `Config.Validate` ones; `Page.Validate`/`Fragment.Validate` stay.

- [ ] **Step 2: CHANGELOG**

Add `## v0.60.0` at the top with `### Changed (breaking)` listing every rename and removal as `old` → `new` (or "removed: what to use instead"), grouped like spec §4–§6, and `### Added` for `collage.Build`, `MultiOrigin`/`OriginFor` on Host, `SlotRule`, `CacheType`, `ErrDuplicateLocalePath`, `ErrAmbiguousActionResult`, `AfterRenderEvent.Path`, the `api` CI job.

- [ ] **Step 3: The plugin sweep list**

```bash
for d in ~/Desktop/collage-*/; do
  n=$(basename "$d"); [ "$n" = collage-docs ] && continue
  hits=$(grep -rnE "collage\.($OLD_IDENTS)" "$d" --include='*.go' 2>/dev/null | wc -l | tr -d ' ')
  [ "$hits" != 0 ] && echo "$n $hits"
done
```

with `OLD_IDENTS` the identifier names of `$OLD` (drop the non-identifier patterns, add `Origins`). Also grep `host\.\(collage\.Origins\)` and `\.RegisterPage(\|\.FragmentURL(` on Host values. Write `docs/superpowers/specs/phase5/plugin-sweep.md`: one row per repo — repo, identifiers it uses, test-only or not. This list, not the inventory's count, drives the plugin releases.

- [ ] **Step 4: Commit**

```bash
gofmt -l . ; go test -count=1 ./... 2>&1 | tail -5
git add CHANGELOG.md docs/superpowers/specs/phase5/plugin-sweep.md <each changed doc and template by name>
git commit -m "docs: v0.60.0 changelog, framework docs on the sealed API, plugin sweep list

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 14: Release (controller only, after the final review and the user's go-ahead to merge)

Not for an implementer subagent: it pushes to shared remotes. Follow the normal release procedure.

- [ ] Merge `feat/phase5-sealing` into main locally; `go test -race -count=1 ./...` on the merged tree.
- [ ] Push main, wait for CI green; tag `v0.60.0`, push the tag. The tag CI's plugins job is expected to go red for every repo in `plugin-sweep.md`.
- [ ] For each repo in `plugin-sweep.md`: in `~/Desktop/collage-<name>`, `GOPROXY=direct GONOSUMDB=github.com/Elagoht go get github.com/Elagoht/collage@v0.60.0`, apply the renames, `go test ./...`, `go run ~/Desktop/collage/cmd/collage plugin check -run .`, bump `Version()`, commit, push, tag (minor bump when its own public API changed, patch otherwise).
- [ ] `gh workflow run ci.yml --ref main`; repeat fixes until all 45 plugin jobs are green.
- [ ] Docs site EN and TR (`~/Desktop/collage-docs`): every old name from Task 13's `$OLD`; bump its go.mod to v0.60.0; `go test ./...`; `go run ./tools/checkcontent`; push.
- [ ] VS Code extension: grep its snippets for old names; release if any changed.
- [ ] Warm the Go proxy for each new plugin tag (both `!elagoht` and `elagoht` paths).
- [ ] Memory: v1-roadmap (phase 5 done), plugin-repos notes.

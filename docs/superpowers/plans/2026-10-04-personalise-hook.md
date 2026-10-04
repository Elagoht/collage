# PersonaliseHook Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Open collage's per-response `personalise` stage to plugins as an optional `PersonaliseHook`, and move elagoht/secure's CSP nonce onto it. That fixes secure's broken nonce when it is listed before elagoht/compress.

**Architecture:**
- collage v0.43.0 adds `plugin.PersonaliseHook` and `plugin.PersonaliseEvent`, plus a registry dispatcher.
- `Handler.personalise` (internal/httpx) runs the hooks after the CSRF substitution and reports a hook error. Its six callers route that error: pages, fragment reads and action HTML become a 500 through `serveFailure`; an error page falls back to `builtinPage`.
- secure v0.2.0 drops its buffering `nonceWriter` and fills the marker in `OnPersonalise`, with the nonce its middleware put in the request context.

**Tech Stack:** Go 1.26, stdlib only.

**Spec:** `docs/superpowers/specs/2026-10-04-personalise-hook-design.md`

## Global Constraints

- Never use the Go type `any` in new code (the existing `// any:` annotated signatures are exempt).
- Additive core:
  - `plugin.Host` and `plugin.ConfigHost` gain no method;
  - no exported signature changes;
  - a site with no `PersonaliseHook` plugin sends byte-identical responses.
- Hooks run in plugin registration order, after the CSRF substitution, before the dev overlay and the dev reload script.
- `Personal == true` is treated like a forgery token:
  - the ETag is recomputed from the sent body;
  - a page is `private, no-store`;
  - a fragment read keeps `private, no-cache` with the recomputed ETag.
- A hook error on a page, fragment read or action is logged and becomes a 500 via `serveFailure`. On an error page it is logged and `builtinPage` for that status is written instead: no recursion, no half-personalised body.
- Work in a git worktree for collage: `git worktree add -b feat/personalise ../collage-personalise main`. Never `git add -A`. Verify with `go test ./... -count=1`, `go test -race -short ./... -count=1`, `go vet ./...`, `staticcheck ./...` and `gofmt -l .` (empty).
- Commit trailer: `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- Nothing is published inside the tasks. Release is a separate phase. secure is tested through a go.work against the worktree, and its go.mod is untouched until release.

## Review Focus

1. **Body changed without `Personal`:** a hook rewrites the body but leaves `Personal` false. The ETag must still name the bytes sent, so recompute it whenever the body changed. Test: `TestPersonalise_ChangedBodyGetsItsOwnETag` (Task 2).
2. **Conditional request on a cached page whose hook makes it personal:** an `If-None-Match` carrying the previous response's ETag must get a 200 with the new body, never a 304. Test: `TestPersonalise_CachedPageIsPersonal` (Task 2).
3. **A hook that panics:** it must not crash the server. A page gets a 500; an error page gets the built-in page. Test: `TestPersonalise_HookPanicIs500` (Task 2).
4. **secure listed before compress (the original bug):** the gzip body's nonce must equal the header's. Test: `TestNonce_WithCompressInEitherOrder` (Task 4).
5. **HEAD on a page carrying a nonce:** no body, no panic, and headers consistent with GET. Test: `TestNonce_Head` (Task 4).

---

### Task 1: Hook types and the registry dispatcher

**Files:**
- Modify: `internal/plugin/hooks.go` (add the interface and event near `AfterRenderHook`)
- Modify: `internal/plugin/registry.go` (add `Personalise` after `AfterRender`)
- Modify: `pkg/collage/collage.go` (aliases after `type AfterRenderEvent = plugin.AfterRenderEvent`)
- Test: `internal/plugin/personalise_test.go` (new)

**Interfaces:**
- Produces:
  - `plugin.PersonaliseHook interface{ OnPersonalise(ctx context.Context, ev *PersonaliseEvent) error }`
  - `plugin.PersonaliseEvent{ Request *http.Request; Header http.Header; Body []byte; Personal bool }`
  - `(*Registry).Personalise(ctx context.Context, ev *PersonaliseEvent) error`
  - `collage.PersonaliseHook`, `collage.PersonaliseEvent`

- [ ] **Step 1: Failing test** `internal/plugin/personalise_test.go`

```go
package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type personaliser struct {
	name    string
	replace string // appended to the body
	err     error
	panics  bool
}

func (p *personaliser) Name() string                     { return p.name }
func (p *personaliser) Version() string                  { return "0" }
func (p *personaliser) Init(context.Context, Host) error { return nil }
func (p *personaliser) Shutdown(context.Context) error   { return nil }
func (p *personaliser) OnPersonalise(_ context.Context, ev *PersonaliseEvent) error {
	if p.panics {
		panic("boom")
	}
	if p.err != nil {
		return p.err
	}
	ev.Body = append(append([]byte(nil), ev.Body...), p.replace...)
	ev.Personal = true
	return nil
}

func TestRegistryPersonalise_InOrder(t *testing.T) {
	r := NewRegistry(nil)
	for _, p := range []Plugin{&personaliser{name: "a", replace: "1"}, &personaliser{name: "b", replace: "2"}} {
		if err := r.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	ev := &PersonaliseEvent{Body: []byte("x")}
	if err := r.Personalise(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if string(ev.Body) != "x12" || !ev.Personal {
		t.Errorf("Body = %q, Personal = %v; want x12, true", ev.Body, ev.Personal)
	}
}

func TestRegistryPersonalise_ErrorAndPanicStop(t *testing.T) {
	for name, first := range map[string]*personaliser{
		"error": {name: "a", err: errors.New("no")},
		"panic": {name: "a", panics: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := NewRegistry(nil)
			_ = r.Register(first)
			_ = r.Register(&personaliser{name: "b", replace: "2"})
			ev := &PersonaliseEvent{Body: []byte("x")}
			err := r.Personalise(context.Background(), ev)
			if err == nil || !strings.Contains(err.Error(), `"a"`) {
				t.Errorf("err = %v, want one naming plugin a", err)
			}
			if string(ev.Body) != "x" {
				t.Errorf("Body = %q: a later hook ran after the failure", ev.Body)
			}
		})
	}
}

func TestRegistryPersonalise_NilAndNone(t *testing.T) {
	var nilRegistry *Registry
	if err := nilRegistry.Personalise(context.Background(), &PersonaliseEvent{}); err != nil {
		t.Errorf("nil registry: %v", err)
	}
}
```

- [ ] **Step 2:** Run `go test ./internal/plugin -run Personalise -count=1`. Expected: FAIL to build (`undefined: PersonaliseEvent`).

- [ ] **Step 3: Implement.** In `hooks.go`, after `AfterRenderHook`:

```go
// PersonaliseHook is implemented by a plugin that rewrites each HTML response for
// the reader it goes to: a CSP nonce, say, that must be new on every response
// while the render behind it is cached and shared.
//
// It runs where collage puts each reader's forgery token into the shared body:
// after the cache, inside every middleware, so before anything compresses the
// body. It is called for a page (from the cache or fresh), a fragment path or
// fragment read, an action's HTML answer and an error page. A document is not
// HTML and gets no call.
type PersonaliseHook interface {
	OnPersonalise(ctx context.Context, ev *PersonaliseEvent) error
}

// PersonaliseEvent is one HTML response on its way to one reader.
type PersonaliseEvent struct {
	// Request is this reader's own request, not a shared render's stripped one.
	Request *http.Request
	// Header is the response's header: set here what must match the body, such
	// as a Content-Security-Policy carrying the nonce put in it.
	Header http.Header
	// Body is what will be written, the reader's forgery token already in it. A
	// hook may replace it; what the cache holds is never changed.
	Body []byte
	// Personal is set by a hook that made Body particular to this reader. The
	// response is then treated as a forgery token makes it: its ETag names the
	// body sent, and a page is answered "private, no-store".
	Personal bool
}
```

Add `"net/http"` to hooks.go's imports if missing. In `registry.go`, after `AfterRender`:

```go
// Personalise dispatches ev to every registered plugin implementing
// PersonaliseHook, in registration order; each sees the body the one before it
// left. It stops and returns a wrapped error at the first hook failure, including
// a contained panic. A nil Registry, or one with no plugin implementing the hook,
// is a no-op that returns nil.
func (r *Registry) Personalise(ctx context.Context, ev *PersonaliseEvent) error {
	for _, p := range r.snapshot() {
		hook, ok := p.(PersonaliseHook)
		if !ok {
			continue
		}
		if err := runHook(p.Name(), "OnPersonalise", func() error { return hook.OnPersonalise(ctx, ev) }); err != nil {
			return err
		}
	}
	return nil
}
```

In `pkg/collage/collage.go`:

```go
// PersonaliseHook is implemented by a plugin that rewrites each HTML response for
// the reader it goes to, after the cache. See plugin.PersonaliseHook.
type PersonaliseHook = plugin.PersonaliseHook

// PersonaliseEvent is one HTML response on its way to one reader.
type PersonaliseEvent = plugin.PersonaliseEvent
```

- [ ] **Step 4:** Run `go test ./internal/plugin ./pkg/collage -count=1`. Expected: PASS.

- [ ] **Step 5:** Commit `feat: PersonaliseHook, and its dispatch` (plus the trailer). Add the four files by name.

### Task 2: Run the hooks in `personalise` and route their failures

**Files:**
- Modify: `internal/httpx/handler.go`:
  - `personalise` (~line 833) gets an error return and the hook call;
  - the fresh-page caller (~774);
  - the `serveCached` caller (~1023).
- Modify: `internal/httpx/action.go`: `writeActionHTML` (~382) and `writeFragmentRead` (~403).
- Modify: `internal/httpx/errorpage.go`: `writeErrorResponse` (~318).
- Modify: `internal/httpx/route.go`: add `failureFor`.
- Test: `pkg/collage/personalise_test.go` (new)

**Interfaces:**
- Consumes: `plugin.PersonaliseEvent`, `(*plugin.Registry).Personalise` (Task 1).
- Produces:
  - `func (h *Handler) personalise(w http.ResponseWriter, r *http.Request, content []byte, etag string) ([]byte, string, bool, error)`
  - `func failureFor(r *http.Request, status int, stage string, err error) failure`

- [ ] **Step 1: Failing tests** `pkg/collage/personalise_test.go`

```go
package collage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// stamp replaces "MARK" with a number new on every response, as a nonce would.
type stamp struct {
	n        atomic.Int32
	personal bool
	fail     error
	panics   bool
}

func (p *stamp) Name() string                             { return "test/stamp" }
func (p *stamp) Version() string                          { return "0" }
func (p *stamp) Init(context.Context, collage.Host) error { return nil }
func (p *stamp) Shutdown(context.Context) error           { return nil }
func (p *stamp) OnPersonalise(_ context.Context, ev *collage.PersonaliseEvent) error {
	if p.panics {
		panic("boom")
	}
	if p.fail != nil {
		return p.fail
	}
	if !bytes.Contains(ev.Body, []byte("MARK")) {
		return nil
	}
	n := strconv.Itoa(int(p.n.Add(1)))
	ev.Body = bytes.ReplaceAll(ev.Body, []byte("MARK"), []byte("n"+n))
	ev.Header.Set("X-Stamp", n)
	ev.Personal = p.personal
	return nil
}

func stampSite(t *testing.T, p *stamp) http.Handler {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/p.html":  {Data: []byte(`<p>MARK</p>`)},
			"t/nf.html": {Data: []byte(`<p>missing MARK</p>`)},
		}, Root: "t"},
		Cache:   collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Plugins: []collage.Plugin{p},
	})
	if err != nil {
		t.Fatal(err)
	}
	frag := collage.NewFragment("p", "p.html").Build()
	if err := app.RegisterPage(collage.NewPage("home").WithContent(frag).WithPath("en", "/").
		WithFragmentPath("en", "/live", frag).Incremental(time.Hour).Build()); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterNotFoundPage(collage.NewPage("nf").WithContent(collage.NewFragment("nf", "nf.html").Build()).Build()); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	return app.Handler()
}

func get(h http.Handler, path, ifNoneMatch string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A cached page goes through the hook on every response, fresh render and cache
// hit alike; made personal, it is private and no-store, and an earlier ETag never
// earns a 304 for a body that changed.
func TestPersonalise_CachedPageIsPersonal(t *testing.T) {
	h := stampSite(t, &stamp{personal: true})
	first := get(h, "/", "")
	second := get(h, "/", first.Header().Get("ETag"))
	if !strings.Contains(first.Body.String(), "<p>n1</p>") || !strings.Contains(second.Body.String(), "<p>n2</p>") {
		t.Fatalf("bodies %q, %q: want n1 then n2", first.Body, second.Body)
	}
	if second.Code != http.StatusOK {
		t.Errorf("second status = %d, want 200 (the body changed)", second.Code)
	}
	for _, rec := range []*httptest.ResponseRecorder{first, second} {
		if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
			t.Errorf("Cache-Control = %q, want private, no-store", cc)
		}
		if rec.Header().Get("X-Stamp") == "" {
			t.Errorf("the hook's header is missing")
		}
	}
	if first.Header().Get("ETag") == second.Header().Get("ETag") {
		t.Errorf("both responses carry ETag %q", first.Header().Get("ETag"))
	}
}

// A hook that rewrites without marking the body personal still gets an ETag that
// names what it sent.
func TestPersonalise_ChangedBodyGetsItsOwnETag(t *testing.T) {
	h := stampSite(t, &stamp{personal: false})
	first := get(h, "/", "")
	second := get(h, "/", first.Header().Get("ETag"))
	if second.Code != http.StatusOK || first.Header().Get("ETag") == second.Header().Get("ETag") {
		t.Errorf("second = %d with ETag %q (first %q): want 200 and a new ETag",
			second.Code, second.Header().Get("ETag"), first.Header().Get("ETag"))
	}
}

// A fragment path and the not-found page go through the hook too.
func TestPersonalise_FragmentAndErrorPage(t *testing.T) {
	h := stampSite(t, &stamp{personal: true})
	if body := get(h, "/live", "").Body.String(); !strings.Contains(body, "<p>n") || strings.Contains(body, "MARK") {
		t.Errorf("fragment path = %q, want it stamped", body)
	}
	nf := get(h, "/nowhere", "")
	if nf.Code != http.StatusNotFound || !strings.Contains(nf.Body.String(), "missing n") {
		t.Errorf("not found = %d %q, want 404 stamped", nf.Code, nf.Body)
	}
}

// A failing hook is a 500 for a page; on the error page it would recurse into,
// the built-in page is written instead, carrying no marker.
func TestPersonalise_HookErrorIs500(t *testing.T) {
	for name, p := range map[string]*stamp{
		"error": {fail: errors.New("no randomness")},
		"panic": {panics: true},
	} {
		t.Run(name, func(t *testing.T) {
			h := stampSite(t, p)
			rec := get(h, "/", "")
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "MARK") {
				t.Errorf("body carries the marker: %q", rec.Body)
			}
			nf := get(h, "/nowhere", "")
			if nf.Code != http.StatusNotFound || strings.Contains(nf.Body.String(), "MARK") {
				t.Errorf("not found = %d %q, want the built-in 404 without the marker", nf.Code, nf.Body)
			}
		})
	}
}
```

Before relying on `app.RegisterNotFoundPage` and `WithFragmentPath`, check their exact use in existing tests (`grep -rn RegisterNotFoundPage pkg/collage/*_test.go`). A not-found page may need no path or a specific builder call; adapt the setup only, never the assertions. `TestPersonalise_HookPanicIs500` from Review Focus 3 is the "panic" subtest of `TestPersonalise_HookErrorIs500`.

- [ ] **Step 2:** Run `go test ./pkg/collage -run Personalise -count=1`. Expected: FAIL. The hook is never called, so `MARK` stays.

- [ ] **Step 3: Implement.**

`internal/httpx/route.go`:

```go
// failureFor is a failure for the route r resolved to, when one did — the
// writer that hit the failure does not have the routeRef at hand.
func failureFor(r *http.Request, status int, stage string, err error) failure {
	if ref, ok := r.Context().Value(routeCtxKey{}).(*routeRef); ok && ref != nil {
		return ref.failure(status, stage, err)
	}
	return failure{status: status, err: err, stage: stage}
}
```

`personalise` in handler.go becomes:

```go
func (h *Handler) personalise(w http.ResponseWriter, r *http.Request, content []byte, etag string) ([]byte, string, bool, error) {
	personal := false
	if h.csrf != nil && h.csrf.Carries(content) {
		token, _, err := h.csrf.TokenFor(r)
		if err != nil {
			// Nothing to substitute with. Serving the marker would render a form
			// that is refused on submission with nothing to explain why, so this
			// is a failure rather than a body.
			h.logger.Error("collage: could not issue a forgery token", "err", err)
		} else {
			content = h.csrf.Personalise(content, token)
			http.SetCookie(w, h.csrf.Cookie(r, token))
			personal = true
		}
	}

	ev := &plugin.PersonaliseEvent{Request: r, Header: w.Header(), Body: content}
	if err := h.plugins.Personalise(r.Context(), ev); err != nil {
		return content, etag, false, err
	}
	changed := personal || !bytes.Equal(ev.Body, content)
	content = ev.Body
	personal = personal || ev.Personal
	// Recomputed whenever the body is not the one the ETag was made from. An
	// ETag that names a body nobody was sent is how a conditional request is
	// answered 304 for content the client never had.
	if changed || ev.Personal {
		etag = cache.ETag(content)
	}
	return content, etag, personal, nil
}
```

Keep the existing doc comment, extended with one paragraph: "Then every PersonaliseHook plugin rewrites the body for this reader, in registration order; a hook that fails is returned as the error, for the caller to answer as its kind of response must." The CSRF-error behaviour (log and keep the marker-free path) is unchanged. Before this change it returned the content untouched; it still does, and the hooks still run. Make sure `bytes` and `plugin` are imported in handler.go.

The callers:

1. Fresh page (~774):

```go
	content, etag, personal, err := h.personalise(w, r, out.content, out.etag)
	if err != nil {
		return h.serveFailure(w, r, route.failure(http.StatusInternalServerError, stagePlugin, err))
	}
```

Use the routeRef variable that function already has in scope (read the function; it is the one whose failures already use `route.failure`). If none is named `route`, use `failureFor(r, …)`.

2. `serveCached` (~1023):

```go
	content, etag, personal, err := h.personalise(w, r, content, etag)
	if err != nil {
		return h.serveFailure(w, r, failureFor(r, http.StatusInternalServerError, stagePlugin, err))
	}
```

3. `writeActionHTML` and `writeFragmentRead` (action.go):

```go
	html, _, _, err := h.personalise(w, r, html, "")
	if err != nil {
		return h.serveFailure(w, r, failureFor(r, http.StatusInternalServerError, stagePlugin, err))
	}
```

Neither function has written headers yet at that point (they set Content-Type after it), so `serveFailure` can still write.

4. `writeErrorResponse` (errorpage.go). This must never recurse:

```go
	personalised, _, _, err := h.personalise(w, r, content, "")
	if err != nil {
		h.logger.Error("collage: a plugin could not personalise the error page; the built-in page is sent instead",
			"status", status, "err", err)
		personalised = builtinPage(failure{status: status, err: err, stage: stagePlugin}, h.devMode)
	}
	content = personalised
```

- [ ] **Step 4:** Run `go test ./pkg/collage -run Personalise -count=1 -v`, then the whole suite, `go test -race -short ./internal/httpx ./pkg/collage -count=1`, vet, staticcheck and gofmt. Expected: all PASS. Existing CSRF tests stay green.

- [ ] **Step 5:** Commit `feat: plugins personalise each HTML response after the cache` (plus the trailer). Add the six files by name.

### Task 3: CHANGELOG, docs, compatibility

**Files:** `CHANGELOG.md`, `docs/plugins.md`, `docs/caching.md`

- [ ] **Step 1:** CHANGELOG `## v0.43.0` / `### Added`, one bullet in house style (read the v0.42.0 entry first).
  - Bold first sentence: **A plugin can rewrite each HTML response after the cache: `PersonaliseHook`.**
  - Say where it runs: after the forgery token, inside every middleware, so before compression.
  - Say which responses it covers, and what `Personal` does.
  - Say that a failure is a 500, or the built-in page on an error page.
  - Say that nothing changes without such a plugin.
  - Name elagoht/secure v0.2.0 as its first user, and the compress-ordering bug it fixes.
- [ ] **Step 2:** `docs/plugins.md`: a "Rewriting each response: `PersonaliseHook`" section beside the other hooks, with the event fields and a short nonce-style example (no `any`). `docs/caching.md`: one paragraph where the forgery-token marker is explained, saying plugins can personalise the same way.
- [ ] **Step 3: Verify.** Run the full suite, `-race -short`, vet, staticcheck and gofmt. Then the plugin-compatibility workspace: all `~/Desktop/collage-*` plugins at their latest tags against `~/Desktop/collage-personalise`. Skip collage-docs, collage-snippets-highlighter and the worktree itself, and build the workspace in a mktemp dir. Expected: no FAIL.
- [ ] **Step 4:** Commit `docs: v0.43.0 PersonaliseHook` (plus the trailer).

### Task 4: elagoht/secure v0.2.0 on the hook

**Files:** `~/Desktop/collage-secure/secure.go`, `secure_test.go`, `README.md`, `collage.json`. Work on branch `feat/personalise` from main. Leave go.mod untouched; test through a go.work in a mktemp dir:

```bash
go work init ~/Desktop/collage-personalise ~/Desktop/collage-secure ~/Desktop/collage-compress
```

compress is needed only by the tests. If secure_test needs compress as a test import, add it to go.mod's require at release, not now: under the workspace it resolves.

**Interfaces:**
- Consumes: `collage.PersonaliseHook`, `collage.PersonaliseEvent` (Task 1).
- Produces: `(*Plugin).OnPersonalise(ctx, ev) error`. The middleware stops wrapping the ResponseWriter.

- [ ] **Step 1: Failing tests** (append to `secure_test.go`; read its existing helpers first and reuse their app setup style)

```go
var nonceIn = regexp.MustCompile(`nonce="([^"]*)"`)

// nonceSite is a cached page with an inline script, behind secure and compress in
// the given order.
func nonceSite(t *testing.T, plugins ...collage.Plugin) http.Handler {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(
			`<html><body><script nonce="{{cspNonce}}">x()</script>` + strings.Repeat("<p>filler</p>", 300) + `</body></html>`)}}, Root: "t"},
		Cache:   collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Minute},
		Plugins: plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	frag := collage.NewFragment("p", "p.html").Build()
	if err := app.RegisterPage(collage.NewPage("home").WithContent(frag).WithPath("en", "/").
		WithFragmentPath("en", "/live", frag).Static().Build()); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	return app.Handler()
}

// fetch returns the decoded body and the nonce the CSP header carries.
func fetch(t *testing.T, h http.Handler, method, path string) (*httptest.ResponseRecorder, string, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Header().Get("Content-Encoding") == "gzip" && body != "" {
		zr, err := gzip.NewReader(strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(zr)
		body = string(b)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	header := ""
	if i := strings.Index(csp, "'nonce-"); i >= 0 {
		header = strings.TrimSuffix(strings.SplitN(csp[i+len("'nonce-"):], "'", 2)[0], "'")
	}
	return rec, body, header
}

func TestNonce_WithCompressInEitherOrder(t *testing.T) {
	csp := secure.Options{CSP: "script-src 'nonce-{nonce}'"}
	orders := map[string][]collage.Plugin{
		"secure first":   {secure.New(csp), compress.New(compress.Options{})},
		"compress first": {compress.New(compress.Options{}), secure.New(csp)},
	}
	for name, plugins := range orders {
		t.Run(name, func(t *testing.T) {
			h := nonceSite(t, plugins...)
			seen := map[string]bool{}
			etags := map[string]bool{}
			for range 2 { // the second is a cache hit
				rec, body, header := fetch(t, h, http.MethodGet, "/")
				m := nonceIn.FindStringSubmatch(body)
				if m == nil || header == "" || m[1] != header {
					t.Fatalf("body nonce %v, header nonce %q: want equal", m, header)
				}
				if strings.Contains(body, "collage-csp-nonce-") {
					t.Fatalf("the marker reached the reader")
				}
				// The core recomputes a personal response's ETag from the body sent
				// (spec): one per response, never a cached one.
				if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
					t.Errorf("Cache-Control %q, want private, no-store", cc)
				}
				seen[header] = true
				etags[rec.Header().Get("ETag")] = true
			}
			if len(seen) != 2 || len(etags) != 2 {
				t.Errorf("two responses shared a nonce or an ETag: nonces %v, etags %v", seen, etags)
			}
		})
	}
}

func TestNonce_FragmentPath(t *testing.T) {
	h := nonceSite(t, secure.New(secure.Options{CSP: "script-src 'nonce-{nonce}'"}))
	_, body, header := fetch(t, h, http.MethodGet, "/live")
	if m := nonceIn.FindStringSubmatch(body); m == nil || m[1] != header {
		t.Errorf("fragment path nonce %v vs header %q", m, header)
	}
}

func TestNonce_Head(t *testing.T) {
	h := nonceSite(t, secure.New(secure.Options{CSP: "script-src 'nonce-{nonce}'"}))
	rec, body, header := fetch(t, h, http.MethodHead, "/")
	if rec.Code != http.StatusOK || body != "" || header == "" {
		t.Errorf("HEAD = %d, body %q, header nonce %q", rec.Code, body, header)
	}
}
```

Add the imports `compress/gzip`, `io`, `regexp` and `compress "github.com/Elagoht/collage-compress"` if missing. Keep every existing test. Where an existing test asserted `nonceWriter`-specific behaviour (Content-Length on HTML, Flush passthrough of buffered HTML), update it to the new behaviour, and list every such change in the report.

- [ ] **Step 2:** Run `GOWORK=<mktemp>/go.work go test ./... -count=1`. Expected: "secure first" FAILS (marker reaches the reader), and "compress first" passes.

- [ ] **Step 3: Implement.**
  - Delete `nonceWriter` and everything only it used.
  - The middleware keeps `p.static(...)`. When `p.opts.CSP != ""`:
    - make a nonce (a 500 on error, as today) and set the CSP header (Report-Only rules unchanged);
    - keep deleting `If-None-Match: *`;
    - call `next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nonceKey{}, nonce)))`;
    - no wrapper.
  - Add:

```go
type nonceKey struct{}

// OnPersonalise puts this response's nonce where {{cspNonce}} left the marker.
// It runs after collage's page cache and inside every middleware, so before a
// compressor: the order plugins are listed in no longer matters.
func (p *Plugin) OnPersonalise(_ context.Context, ev *collage.PersonaliseEvent) error {
	if p.marker == "" || !bytes.Contains(ev.Body, []byte(p.marker)) {
		return nil
	}
	nonce, _ := ev.Request.Context().Value(nonceKey{}).(string)
	if nonce == "" {
		// Not through this plugin's middleware, or no CSP configured: there is
		// no header to match, so make the nonce and the header here.
		fresh, err := newNonce()
		if err != nil {
			return fmt.Errorf("secure: no randomness for a nonce: %w", err)
		}
		nonce = fresh
		if p.opts.CSP != "" {
			name := "Content-Security-Policy"
			if p.opts.CSPReportOnly || p.dev {
				name += "-Report-Only"
			}
			ev.Header.Set(name, strings.ReplaceAll(p.opts.CSP, "{nonce}", nonce))
		}
	}
	ev.Body = bytes.ReplaceAll(ev.Body, []byte(p.marker), []byte(nonce))
	ev.Personal = true
	return nil
}
```

  - Add the compile-time assertion `var _ collage.PersonaliseHook = (*Plugin)(nil)`.
  - Update the package doc's "Nonces and the page cache" section: the nonce goes in after the cache, before compression; the core answers the page private and no-store.
  - `Version()` returns "0.2.0". Mention the compress-order fix in the README.
  - Regenerate collage.json:

```bash
D=$(mktemp -d)
ln -s ~/Desktop/collage-secure $D/collage-secure
GOWORK=off go run -C ~/Desktop/collage-snippets-highlighter/tools/schemagen . -src $D -manifests -out $D/schema.json
```

    Expect only descriptions to change.

- [ ] **Step 4:** Run `GOWORK=<mktemp>/go.work go test ./... -count=1 -race`, vet, staticcheck and gofmt. Expected: all PASS.

- [ ] **Step 5:** Commit on feat/personalise: `feat!: the nonce goes in after the cache, before compression` (plus the trailer). The `!` is there because secure now requires collage v0.43.0.

### Task 5: Roadmap log and docs-site preparation

**Files:**
- `~/Desktop/collage-personalise/docs/superpowers/plans/2026-10-03-v1-roadmap.md`: rows in the Turkish core-change log table.
- Docs site `~/Desktop/collage-docs`, branch `docs/v0.43.0` from main: the plugin-writing guide gets the hook, and secure's entry gets the fix (EN and TR). Leave go.mod untouched (bumped at release).

- [ ] **Step 1:** Roadmap rows (Turkish, matching the table):
  - `| csp-nonce | PersonaliseHook (personalise aşaması plugin'lere açıldı); ayrı plugin yazılmadı | eklemeli | v0.43.0 |`
  - `| csp-nonce | (bulundu) secure, compress'ten önce listelenince nonce'u yerleştiremiyordu | düzeltme (secure v0.2.0) | — |`

  Commit in the worktree: `docs: the csp-nonce rows of the core-change log`.
- [ ] **Step 2:** Docs site:
  1. `git status` (clean);
  2. `git checkout -b docs/v0.43.0`;
  3. edit EN and TR to the same structure;
  4. run `go test ./... -count=1` (heading parity);
  5. commit `docs: v0.43.0 PersonaliseHook and secure v0.2.0`.

  Do not push.

## Release (after the final review, controller-run)

1. Merge feat/personalise into collage main. Run the tests, tag v0.43.0, push, watch the tag CI. Verify that `go get` works.
2. secure:
   1. `go get collage@v0.43.0`, plus compress at its latest tag if the tests import it;
   2. `go mod tidy`;
   3. `GOWORK=off go test -race`;
   4. commit, ff-merge, push, tag v0.2.0.
3. Docs site:
   1. `go get collage@v0.43.0` and run the tests;
   2. merge docs/v0.43.0 and push.
4. Remove the worktree and branches, and update memory.

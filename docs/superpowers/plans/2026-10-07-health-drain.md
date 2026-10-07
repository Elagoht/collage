# Health and Drain Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On shutdown, collage keeps serving for a configurable drain while plugins learn of it. A new plugin, `elagoht/health`, serves liveness and readiness endpoints, runs application checks, and can shed load.

**Architecture:**
- The core adds `ServerConfig.DrainDelay` and an optional `plugin.DrainHook`. Shutdown becomes: drain (OnDrain, keep-alives off, wait), then today's sequence.
- The new repo `~/Desktop/collage-health` implements middleware endpoints, cached parallel checks, an in-flight cap, and `DrainHook`. During development it builds against the core worktree through `replace`.

**Tech Stack:** Go 1.26, stdlib only (net/http, context, sync, time, encoding/json).

**Spec:** `docs/superpowers/specs/2026-10-07-health-drain-design.md`

## Global Constraints

- Never use the type `any`. Generic constraints are fine.
- Defaults change nothing: `DrainDelay` 0 must behave exactly as today. That means the shutdown order (streams → `server.Shutdown` → plugins `Shutdown`), idempotence, and every existing test.
- `Host` does not grow. No public `Draining()` query.
- Plugin conventions:
  - module `github.com/Elagoht/collage-health`, `go 1.26`, a comment above `module`;
  - `const Name = "elagoht/health"`, `New()` / `NewWith(cfg)`, `Version()` `"0.1.0"`;
  - README titled `# elagoht/health`, `.gitignore`, a `collage.json` like ~/Desktop/collage-errortrack/collage.json;
  - no LICENSE file.
- Plugin defaults: `livePath` `/healthz`, `readyPath` `/readyz`, `checkTimeout` 2s, `cacheFor` 1s, `details` false, `maxInFlight` 0.
- Bodies:
  - plain text `ok` / `unavailable` / `draining`;
  - with `details`, JSON `{"status":…,"checks":{name:"ok"|"failing"}}`, names sorted;
  - never an error's text.
- Endpoints answer GET and HEAD only, with `Cache-Control: no-store`. Any other method gets 405 with `Allow: GET, HEAD`.
- Shedding: 503 with `Retry-After: 1`. Endpoints and `collage.IsCapture` requests are exempt.
- Tests: core `go test -count=1 ./...`; plugin `GOWORK=off go test -count=1 ./...`; vet, gofmt and staticcheck clean.
- Commits:
  - stage files by name after reading `git status`;
  - end with the trailer `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`;
  - no push and no tag during the plan.

## Review Focus

1. **A second signal during the drain.** Ctrl-C twice must end the wait at once, not after `DrainDelay`. Pinned in Task 2, `TestDrain_SecondSignalCutsTheWait`.
2. **A kept-alive client during the drain.** It must be told to reconnect (`Connection: close`) while still being served. Pinned in Task 2, `TestDrain_ServesAndClosesKeepAlives`.
3. **A check that ignores its ctx and blocks forever.** Readiness must still answer within `checkTimeout`, and the next probe after `cacheFor` must not wait on the stuck one. Pinned in Task 4, `TestCheck_StuckCheckDoesNotBlockProbes`.
4. **Overload with `maxInFlight`.** The readiness probe itself must never be refused, and a refused request must not leak the counter. Pinned in Task 5, `TestShed_EndpointsExemptAndCounterBalanced`.
5. **The health path also registered as a page.** Init must refuse it with a message naming the page, not silently hide the page. Pinned in Task 3, `TestInit_PathClaimedByPage`.

---

### Task 1: Core: the `DrainHook` interface and its dispatch

Work in a worktree: `git -C ~/Desktop/collage worktree add ../collage-health-core -b feat/health`. All core paths below are relative to `~/Desktop/collage-health-core`.

**Files:**
- Modify: `internal/plugin/hooks.go` (add `DrainHook` next to `StreamCloser`)
- Modify: `internal/plugin/registry.go` (add `Drain()` and `HasDrainHook()` next to `CloseStreams`)
- Modify: `pkg/collage/collage.go` (alias `DrainHook`, next to the `StreamCloser` alias; find it with grep)
- Test: `internal/plugin/drain_test.go`

**Interfaces:**
- Produces:
  - `type DrainHook interface{ OnDrain() }`
  - `func (r *Registry) Drain()`: calls OnDrain in registration order, contains panics, and a nil Registry is a no-op.
  - `collage.DrainHook` alias.

- [ ] **Step 1: Write the failing test** (`internal/plugin/drain_test.go`). Copy the plugin-stub pattern from the existing registry tests (grep `CloseStreams` in `internal/plugin/*_test.go` and reuse its stub's `Name/Version/Init/Shutdown`).

```go
package plugin

import "testing"

type drainStub struct {
	stub // reuse the existing test stub type that satisfies Plugin; rename if it differs
	calls *[]string
	name  string
	panic bool
}

func (d drainStub) OnDrain() {
	*d.calls = append(*d.calls, d.name)
	if d.panic {
		panic("boom")
	}
}

func TestRegistry_DrainInOrderAndContainsPanics(t *testing.T) {
	var calls []string
	r := newTestRegistry(t, // the existing helper that registers plugins; adapt to what exists
		drainStub{name: "a", calls: &calls, panic: true},
		drainStub{name: "b", calls: &calls},
	)
	r.Drain()
	if len(calls) != 2 || calls[0] != "a" || calls[1] != "b" {
		t.Fatalf("OnDrain calls = %v, want [a b] despite a's panic", calls)
	}
	var nilReg *Registry
	nilReg.Drain() // must not panic
}
```

Adapt the stub and helper names to what the package's tests already use. Keep both assertions.

- [ ] **Step 2: Run it and check that it fails.** Run `go test -count=1 ./internal/plugin/ -run Drain`. Expect a FAIL because `Drain` and `OnDrain` are undefined.

- [ ] **Step 3: Implement.** In `hooks.go`, directly after `StreamCloser`:

```go
// DrainHook is implemented by a plugin that must know the server is about to
// stop taking traffic: readiness turns false, new work is refused. OnDrain is
// called once, when the drain starts and before ServerConfig.DrainDelay; the
// server still serves requests until the delay ends. It must not block.
type DrainHook interface {
	OnDrain()
}
```

In `registry.go`, directly after `CloseStreams`:

```go
// Drain tells every plugin implementing DrainHook that the drain has started, in
// registration order. A panic in one is contained, so the others still hear of
// it. A nil Registry does nothing.
func (r *Registry) Drain() {
	for _, p := range r.snapshot() {
		if hook, ok := p.(DrainHook); ok {
			_ = safeCall(func() error { hook.OnDrain(); return nil })
		}
	}
}
```

Check that `snapshot()` handles a nil receiver the way `CloseStreams` relies on. If it doesn't, add `if r == nil { return }`. Add `DrainHook = plugin.DrainHook` beside the `StreamCloser` alias in `pkg/collage`.

- [ ] **Step 4: Run** `go test -count=1 ./internal/plugin/ ./pkg/collage/`. Expect PASS.

- [ ] **Step 5: Commit** `feat: DrainHook tells plugins a drain has started`

---

### Task 2: Core: the drain phase in `Shutdown` and `ListenAndServe`

**Files:**
- Modify: `internal/core/app.go`:
  - `ServerConfig` (around line 190): add `DrainDelay` after `ShutdownTimeout`;
  - `App` fields (around line 397–430): add `drainOnce sync.Once`, `hurry chan struct{}`, `hurryOnce sync.Once`, `listenAddr net.Addr`;
  - the constructor: initialise `hurry`;
  - `ListenAndServe` (around line 923–991);
  - `shutdown` (around line 1019).
- Modify: `pkg/collage/config.go` (`ServerConfig` doc, if it re-documents fields)
- Modify: `docs/deployment.md` (a "Graceful shutdown" section), `CHANGELOG.md` (`## Unreleased` → Added)
- Test: `internal/core/drain_test.go`

**Interfaces:**
- Consumes: `(*plugin.Registry).Drain()` (Task 1).
- Produces: `ServerConfig.DrainDelay time.Duration`. No new exported functions.

**Behaviour, from spec §1:**
- `drain(ctx)` runs at most once (`drainOnce`):
  1. `a.plugins.Drain()`;
  2. if a server exists, `server.SetKeepAlivesEnabled(false)`;
  3. if a server exists, `DrainDelay > 0` and `!a.DevMode()`, wait for whichever comes first of `time.After(DrainDelay)`, `ctx.Done()` and `a.hurry`.
- `shutdown(ctx)` calls `a.drain(ctx)` first, then does exactly what it does today.
- `ListenAndServe`, on the first signal:
  1. log `collage: draining` with `delay`;
  2. start a goroutine that waits for a second signal and then calls `a.hurryNow()` (`hurryOnce.Do(close(hurry))`). It must exit when `ListenAndServe` returns: select on the signals channel and on a `done` channel closed by a defer;
  3. call `a.drain(context.Background())`;
  4. only then create the `ShutdownTimeout` ctx and call `a.Shutdown(ctx)`. `drainOnce` makes the inner drain a no-op.
- Store `listener.Addr()` in `a.listenAddr` under `a.mu` before closing `listening`, so tests can dial.
- In `New` (or wherever ServerConfig is validated), log a warning when `DrainDelay > 0 && ShutdownTimeout == 0`: `"collage: DrainDelay is set but ShutdownTimeout is 0; in-flight requests get no time to finish"`. Return no error.

- [ ] **Step 1: Write the failing tests** (`internal/core/drain_test.go`):

```go
package core

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type drainSpy struct {
	// embed whatever minimal Plugin test stub internal/core tests already use
	// (grep "Shutdown(context.Context) error" in internal/core/*_test.go)
	testPlugin
	drained atomic.Int32
}

func (d *drainSpy) OnDrain() { d.drained.Add(1) }

func startServing(t *testing.T, mutate func(*Config), plugins ...plugin.Plugin) (*App, string, chan error) {
	t.Helper()
	app := newTestApp(t, func(c *Config) {
		c.Plugins = append(c.Plugins, plugins...)
		if mutate != nil {
			mutate(c)
		}
	})
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- app.ListenAndServe() }()
	waitListening(t, app)
	app.mu.Lock()
	addr := app.listenAddr.String()
	app.mu.Unlock()
	return app, "http://" + addr, served
}

// During the drain the server still answers, and tells a kept-alive client to
// reconnect; OnDrain runs once; the port closes only after the delay.
func TestDrain_ServesAndClosesKeepAlives(t *testing.T) {
	spy := &drainSpy{}
	app, base, served := startServing(t, func(c *Config) { c.Server.DrainDelay = 300 * time.Millisecond }, spy)

	shut := make(chan error, 1)
	start := time.Now()
	go func() { shut <- app.Shutdown(context.Background()) }()
	time.Sleep(50 * time.Millisecond)

	res, err := http.Get(base + "/")
	if err != nil {
		t.Fatalf("request during the drain: %v", err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !res.Close {
		t.Fatalf("during the drain: status %d, Close %v; want 200 and Connection: close", res.StatusCode, res.Close)
	}
	if err := <-shut; err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("shutdown took %v, want at least the 300ms drain", elapsed)
	}
	if n := spy.drained.Load(); n != 1 {
		t.Fatalf("OnDrain called %d times, want 1", n)
	}
	<-served
	if _, err := net.DialTimeout("tcp", base[len("http://"):], 200*time.Millisecond); err == nil {
		t.Fatal("port still open after shutdown")
	}
}

// A done ctx cuts the wait short.
func TestDrain_ContextCutsTheWait(t *testing.T) {
	app, _, served := startServing(t, func(c *Config) { c.Server.DrainDelay = time.Hour })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	app.Shutdown(ctx) // its error may report the cancelled ctx; only the time matters
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Shutdown waited %v on a 100ms ctx", elapsed)
	}
	<-served
}

// Ctrl-C twice: the second signal ends the drain at once.
func TestDrain_SecondSignalCutsTheWait(t *testing.T) {
	_, _, served := startServing(t, func(c *Config) {
		c.Server.DrainDelay = time.Hour
		c.Server.ShutdownTimeout = time.Second
	})
	self, _ := os.FindProcess(os.Getpid())
	start := time.Now()
	self.Signal(syscall.SIGTERM)
	time.Sleep(100 * time.Millisecond)
	self.Signal(syscall.SIGTERM)
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("a second signal did not end the drain")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("too slow")
	}
}

// DrainDelay 0 is today's behaviour: no wait, OnDrain still runs once.
func TestDrain_ZeroDelayDoesNotWait(t *testing.T) {
	spy := &drainSpy{}
	app, _, served := startServing(t, nil, spy)
	start := time.Now()
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-served
	if time.Since(start) > time.Second || spy.drained.Load() != 1 {
		t.Fatalf("zero delay: took %v, OnDrain %d", time.Since(start), spy.drained.Load())
	}
}

// Development mode skips the wait.
func TestDrain_DevModeSkipsTheWait(t *testing.T) {
	app, _, served := startServing(t, func(c *Config) {
		c.Server.DrainDelay = time.Hour
		c.DevMode = true
	})
	start := time.Now()
	app.Shutdown(context.Background())
	<-served
	if time.Since(start) > 2*time.Second {
		t.Fatal("development mode waited for DrainDelay")
	}
}

// Shutdown before anything serves: OnDrain runs, no wait.
func TestDrain_BeforeServingDoesNotWait(t *testing.T) {
	spy := &drainSpy{}
	app := newTestApp(t, func(c *Config) {
		c.Server.DrainDelay = time.Hour
		c.Plugins = append(c.Plugins, spy)
	})
	start := time.Now()
	app.Shutdown(context.Background())
	if time.Since(start) > time.Second || spy.drained.Load() != 1 {
		t.Fatalf("took %v, OnDrain %d", time.Since(start), spy.drained.Load())
	}
}
```

Adapt `testPlugin` and `plugin.Plugin` to the names `internal/core` tests already use. Grep for an existing test plugin that implements `StreamCloser`; if none exists, define a minimal one with `Name/Version/Init/Shutdown`. `c.Plugins` must be the config field the tests already use to register plugins. Add a warning test only if `internal/core` tests already capture the logger (grep `slog.New` in its tests); otherwise skip that test.

- [ ] **Step 2: Run and check they fail.** `go test -count=1 ./internal/core/ -run Drain`. Expect FAIL: `DrainDelay` and `listenAddr` are undefined.

- [ ] **Step 3: Implement as described in "Behaviour".**
  - Wait helper:

```go
// drain runs the one drain: plugins hear of it, kept-alive connections are
// closed after their next response, and — with a server serving, a DrainDelay
// and not in development — the server keeps serving for DrainDelay, or until
// ctx is done or a second signal arrives.
func (a *App) drain(ctx context.Context) {
	a.drainOnce.Do(func() {
		a.plugins.Drain()
		a.mu.Lock()
		server := a.server
		a.mu.Unlock()
		if server == nil {
			return
		}
		server.SetKeepAlivesEnabled(false)
		delay := a.cfg.Server.DrainDelay
		if delay <= 0 || a.DevMode() {
			return
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
		case <-a.hurry:
		}
	})
}

func (a *App) hurryNow() { a.hurryOnce.Do(func() { close(a.hurry) }) }
```

  - The signal branch of `ListenAndServe` becomes:

```go
	case <-signals:
		a.Logger().Info("collage: draining", "delay", a.cfg.Server.DrainDelay)
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-signals:
				a.hurryNow()
			case <-done:
			}
		}()
		a.drain(context.Background())
		a.Logger().Info("collage: shutting down", "timeout", a.cfg.Server.ShutdownTimeout)
		ctx, cancel := context.WithTimeout(context.Background(), a.cfg.Server.ShutdownTimeout)
		defer cancel()
		shutdownErr := a.Shutdown(ctx)
		a.Logger().Info("collage: stopped")
		return errors.Join(shutdownErr, cleanStop(<-served))
```

  - `shutdown(ctx)`: put `a.drain(ctx)` as the first statement, before setting `closing`. Leave every existing line after it untouched. `a.closing` is still set before the server stops, which is fine: the drain does not read `closing`.
  - Update the doc comments of `ListenAndServe`, `Shutdown` and `ServerConfig.DrainDelay`:

```go
	// DrainDelay is how long the server keeps serving after a shutdown starts,
	// so a load balancer can see readiness fail and stop sending traffic before
	// the port closes. Plugins implementing DrainHook are told when it starts.
	// It comes before ShutdownTimeout, which then bounds the in-flight requests,
	// so a stop can take DrainDelay + ShutdownTimeout. Zero, the default, does
	// not wait. It is ignored in development mode.
	DrainDelay time.Duration
```

- [ ] **Step 4: Run** `go test -count=1 ./...` (the whole core) and `go vet ./...`. Expect PASS, with every existing shutdown test unchanged.

- [ ] **Step 5: Docs.**
  - `docs/deployment.md`: add a section "Graceful shutdown and draining". It covers:
    - the order (drain → streams → server → plugins);
    - `DrainDelay` and `ShutdownTimeout` adding up;
    - systemd `TimeoutStopSec` greater than their sum;
    - Kubernetes `terminationGracePeriodSeconds` greater than their sum, plus readiness and liveness probes pointing at `elagoht/health`'s `/readyz` and `/healthz`;
    - Ctrl-C twice skips the wait;
    - development mode ignores the delay.
  - `CHANGELOG.md` `## Unreleased` → `### Added`: `ServerConfig.DrainDelay` and `DrainHook`, noting that nothing changes by default.

- [ ] **Step 6: Commit** `feat: a drain phase before shutdown keeps serving for DrainDelay`

---

### Task 3: Plugin repo, configuration, endpoints and drain flag

**Files (new repo `~/Desktop/collage-health`, `git init`):**
- Create: `go.mod`. It has a comment above `module`, `go 1.26`, `require github.com/Elagoht/collage v0.52.0`, and `replace github.com/Elagoht/collage => ../collage-health-core`.
- Create: `health.go` (Plugin, Options, Duration, Init validation, middleware, endpoints, OnDrain)
- Create: `health_test.go`
- Create: `README.md`, `.gitignore`, `collage.json`

**Interfaces:**
- Consumes: `collage.DrainHook` (Task 1), `collage.PluginConfig`, `collage.Host` (`Use`, `Pages`, `Logger`).
- Produces (Tasks 4 and 5 rely on these):

```go
const Name = "elagoht/health"
type Duration time.Duration // UnmarshalJSON: "2s" or nanoseconds (copy collage-errortrack/options.go)
type Options struct {
	LivePath     string   `json:"livePath"`
	ReadyPath    string   `json:"readyPath"`
	CheckTimeout Duration `json:"checkTimeout"`
	CacheFor     Duration `json:"cacheFor"`
	Details      bool     `json:"details"`
	MaxInFlight  int      `json:"maxInFlight"`
}
func New() *Plugin
func NewWith(opts Options) *Plugin
func (p *Plugin) Check(name string, fn func(context.Context) error) // Task 4
func (p *Plugin) OnDrain()
// internal: p.ready(ctx) (status string, checks map[string]bool) — Task 4 fills checks
```

- [ ] **Step 1: Write the failing tests** in `health_test.go`, using a real app the same way ~/Desktop/collage-ratelimit/ratelimit_test.go's `site()` helper does: `collage.New` plus `RegisterPage`, and `app.Handler()` served through `httptest`.
  - `TestLiveness`: GET `/healthz` returns 200 with body `ok` and `Cache-Control: no-store`. HEAD returns 200 with no body. POST returns 405 with `Allow: GET, HEAD`.
  - `TestReadinessAndDrain`: `/readyz` returns 200 `ok`. After `p.OnDrain()` it returns 503 `draining`, while a page (`/`) still returns 200.
  - `TestCustomPaths`: Options `LivePath: "/live"`, `ReadyPath: "/ready"` are served there, and `/healthz` falls through to the app (404).
  - `TestInit_Validation`, table-driven, where each case must make `app.Handler()` or `app.Start()` fail with an error mentioning `elagoht/health`:
    - equal paths;
    - `livePath` without a leading `/`;
    - negative `checkTimeout`;
    - negative `maxInFlight`.
  - `TestInit_PathClaimedByPage` (Review Focus 5): a page registered at `/healthz` makes Init fail with an error naming the page.
  - `TestConfigFromJSON`: `PluginConfig` `{"elagoht/health":{"readyPath":"/r","checkTimeout":"500ms"}}` serves `/r`, and an unknown key fails as collage reports unknown keys.

- [ ] **Step 2: Run** `GOWORK=off go test -count=1 ./...` and expect FAIL.

- [ ] **Step 3: Implement** `health.go`:
  - `Init(ctx, host)`:
    1. `opts, err := collage.PluginConfig(host, p.opts)`.
    2. Apply defaults for zero values.
    3. Validate, prefixing each error with `elagoht/health: `.
    4. Check page paths: for each `host.Pages()` and each `page.Paths` value, compare the pattern with each endpoint path. Compare literally and with a trailing slash trimmed. On a match, fail with `elagoht/health: %s is also page %q's path; the middleware answers first, so the page could never be reached`.
    5. Set `p.inited = true` (an `atomic.Bool`; `Check` reads it in Task 4).
    6. Call `host.Use(p.middleware)`.
  - The middleware: if `r.URL.Path == livePath` or `readyPath`, the endpoint answers. Otherwise it calls `next`; Task 5 adds shedding here.
  - Liveness writes `ok`. Readiness calls `p.ready(r.Context())`, which for now returns `"draining"` if `p.draining.Load()`, else `"ok"` with no checks; Task 4 adds checks and details.
  - `OnDrain` sets `p.draining.Store(true)`.
  - Add compile-time assertions: `var _ collage.Plugin = (*Plugin)(nil)` and `var _ collage.DrainHook = (*Plugin)(nil)`.
  - `Shutdown` returns nil.
  - Write README:
    - what the plugin is;
    - list it FIRST among plugins;
    - the endpoints;
    - configuration;
    - a Kubernetes probe snippet;
    - `DrainDelay` with a link to collage docs;
    - "requires collage v0.53.0".
  - Write `.gitignore` (`*.test`, `*.out`) and `collage.json` (`$schema`, name, description, repository, `config.properties` for the six options, `additionalProperties: false`).

- [ ] **Step 4: Run tests, vet, gofmt.** Expect PASS.

- [ ] **Step 5: Commit** (in the new repo) `feat: liveness and readiness endpoints, readiness false while draining`

---

### Task 4: Plugin checks: parallel, timed, cached, shared, safe

**Files:**
- Create: `check.go` (check registry, run, cache, single flight)
- Modify: `health.go` (`ready` uses checks; `details` JSON body)
- Test: `check_test.go`

**Interfaces:**
- Consumes: `Options.CheckTimeout`, `Options.CacheFor`, `Options.Details`, `p.inited`, `p.draining` (Task 3).
- Produces: `func (p *Plugin) Check(name string, fn func(context.Context) error)`.

**Behaviour:**
- `Check` panics on an empty name, a duplicate name, a nil fn, or a call after Init.
- A readiness probe returns the cached result when it is younger than `cacheFor`.
  - Otherwise, if a run is in flight, the probe waits for it.
  - Otherwise it starts a run.
- A run executes all checks in parallel. Each check gets a goroutine with `context.WithTimeout(context.Background(), checkTimeout)`, independent of the probe's request, so one probe disconnecting does not fail the shared run.
  - A check's result is `ok` iff fn returned nil before the timeout.
  - A panic is recovered and counts as failing; log it with the check name.
  - An error is logged (name and error) and counts as failing.
- The run waits for each check until its own deadline, then marks a still-running check as failing and discards its later result. Use a buffered channel per check, so a stuck goroutine can finish later without blocking anyone.
- The overall status is `draining` if draining (checks are still not run), else `unavailable` if any check failed, else `ok`.
- With `details: true` the body is JSON:

```go
type report struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"` // encoding/json sorts map keys
}
```

- [ ] **Step 1: Write the failing tests** (`check_test.go`):
  - `TestCheck_FailingMakesUnavailable`: a check returning an error gives 503 `unavailable`, and the error text does not appear in the body.
  - `TestCheck_PanicIsFailing`: a panicking check gives 503 and the process survives.
  - `TestCheck_TimeoutIsFailing`: a check that sleeps 200ms with `checkTimeout` 50ms gives 503 within about 150ms.
  - `TestCheck_StuckCheckDoesNotBlockProbes` (Review Focus 3): a check that blocks forever and ignores its ctx, with `checkTimeout` 50ms and `cacheFor` 10ms.
    - Three sequential probes 30ms apart each answer 503 within 200ms.
    - The test counts how many times the check was entered. It must be at most the number of runs, and no probe waits on the stuck goroutine.
    - At the end, unblock the check through a channel the test closes, so no goroutine leaks.
  - `TestCheck_ConcurrentProbesShareOneRun`: 20 parallel probes against a check that counts calls and takes 50ms result in a call count of 1.
  - `TestCheck_CacheFor`: with `cacheFor` 100ms, two probes 10ms apart give count 1, and a third after 150ms gives count 2.
  - `TestCheck_Details`: with `details: true` and checks `db` (failing) and `cache` (ok), the body is exactly `{"status":"unavailable","checks":{"cache":"ok","db":"failing"}}`. Draining with details gives `{"status":"draining"}`.
  - `TestCheck_Misuse`: an empty name, a duplicate, nil fn, and a call after Init each panic. Use `recover` in a helper that asserts the panic.

- [ ] **Step 2: Run** and expect FAIL.

- [ ] **Step 3: Implement** `check.go`. A `sync.Mutex` guards `last time.Time`, `lastResult map[string]bool` and `inflight chan struct{}`. A probe:
  1. locks;
  2. returns `lastResult` if it is fresh;
  3. if `inflight != nil`, copies it, unlocks, waits on it (or on the request ctx), then re-reads;
  4. otherwise creates `inflight`, unlocks, runs, locks, stores the result, closes `inflight` and sets it to nil.

  No `any`. No `x/sync` dependency; write the single flight by hand as above.

- [ ] **Step 4: Run** `GOWORK=off go test -count=1 -race ./...`, vet, gofmt. Expect PASS.

- [ ] **Step 5: Commit** `feat: application checks for readiness, run in parallel, timed, cached and shared`

---

### Task 5: Plugin load shedding, export, end to end

**Files:**
- Modify: `health.go` (shedding in the middleware)
- Test: `shed_test.go`, `e2e_test.go`

**Interfaces:**
- Consumes: `Options.MaxInFlight` and the middleware (Task 3); `collage.IsCapture`; `collage.NewBuilder`; core `ServerConfig.DrainDelay` (Task 2).

**Behaviour:**
- When `MaxInFlight > 0`, the middleware keeps an `atomic.Int64` counter.
  - The plugin's endpoints and `collage.IsCapture(r.Context())` requests skip the counter entirely.
  - Any other request increments the counter. If the new value exceeds `MaxInFlight`, it decrements and answers 503 with `Retry-After: 1`, `Cache-Control: no-store` and body `overloaded`.
  - Otherwise `defer` the decrement and call next. The deferred decrement must run even if next panics.

- [ ] **Step 1: Write the failing tests:**
  - `TestShed_EndpointsExemptAndCounterBalanced` (Review Focus 4): with `maxInFlight` 2 and a page handler that blocks on a channel:
    1. Start 2 requests and wait until both are inside.
    2. A 3rd page request gets 503 with `Retry-After: 1`.
    3. `/readyz` and `/healthz` both get 200.
    4. Release the blocked requests.
    5. A new page request gets 200, so the counter returned to 0. Assert it by making 2 more concurrent blocked requests succeed in entering.
    6. A page whose handler panics, followed by checking the counter again, shows the panic did not leak a slot.

    To make a page block, register a page whose data handler waits on a channel. Copy the data-handler page pattern from collage's `pkg/collage` tests (grep `WithHandler` or `DataHandler` in `pkg/collage/*_test.go`).
  - `TestShed_Off`: with `maxInFlight` 0, 50 concurrent requests all get 200.
  - `TestExport_NoEndpoints`:
    1. Build with `collage.NewBuilder(app, collage.BuildOptions{OutDir: dir})` and `Build`.
    2. Assert that no file named `healthz`, `readyz` or `healthz/index.html` exists in `dir`.
    3. With `maxInFlight` 1 and a buildReader plugin, as in ~/Desktop/collage-ratelimit/capture_test.go, every captured file has status 200. Capture requests are not shed.
  - `TestE2E_DrainOverRealServer`:
    1. Start `app.ListenAndServe()` with `Server.Port 0` and `Server.DrainDelay` 300ms in a goroutine.
    2. Find the address. Core keeps it unexported, so poll `app.Handler` readiness and read the address from the log line "collage: listening", using a `slog` handler that captures `addr`. Alternatively pick a free port first with `net.Listen("tcp","127.0.0.1:0")`, close it, and use that port.
    3. Call `app.Shutdown(ctx)` in a goroutine.
    4. Within the drain: `/readyz` returns 503 `draining` and `/` returns 200.
    5. After `Shutdown` returns, dialing fails.

- [ ] **Step 2: Run** and expect FAIL.

- [ ] **Step 3: Implement shedding.**

- [ ] **Step 4: Run** `GOWORK=off go test -count=1 -race ./...`, vet, gofmt and staticcheck. Expect PASS.

- [ ] **Step 5: README.** Add the shedding section: global cap, no queue, endpoints and captures exempt.

- [ ] **Step 6: Commit** `feat: an in-flight cap sheds load, probes and build captures exempt`

---

## After this plan

Release in this order, following the normal procedure. The user authorised it for the deploy adapter; confirm again before the first push of this release.

1. Merge `feat/health` into collage main. CHANGELOG `## v0.53.0`. Push main, wait for CI to go green, tag `v0.53.0`, then wait for the tag CI to go green.
2. collage-health:
   1. Drop the `replace`.
   2. Run `GOPROXY=direct GONOSUMDB=github.com/Elagoht go get github.com/Elagoht/collage@v0.53.0`, tidy and test.
   3. Run `gh repo create Elagoht/collage-health --public`, then a plain `git push`, then tag `v0.1.0`.
   4. Verify with `go get` in a scratch module.
3. Add `collage-health` to the CI matrix in `.github/workflows/ci.yml`, after `collage-flash`, in its own commit. Push, then run `gh workflow run ci.yml --ref main` and watch it.
4. Docs site EN+TR: deployment/shutdown, plugins catalogue (41), and writing-plugins `DrainHook`.
5. Extension schema:
   - add a schemagen `decoders` entry for `health.Duration`;
   - shallow-clone every plugin into the scratchpad;
   - `npm run schema`, `npm test`;
   - release 0.11.0 with the vsix.
6. Update the memory files (`v1-roadmap.md`, `plugin-repos.md`).

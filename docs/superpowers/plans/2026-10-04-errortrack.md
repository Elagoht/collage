# elagoht/errortrack Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Report collage's 5xx failures and panics to Sentry, or any Sentry-protocol service, privately and without blocking requests. The core change behind it gives `ErrorEvent` its status and request, and reports request-level panics as a typed `*PanicError`.

**Architecture:**
- **collage v0.45.0** adds `ErrorEvent.Status` and `ErrorEvent.Request`. It reports the request-level recover as `fmt.Errorf("%w: %w", ErrPanic, &render.PanicError{…})`, and logs the stack as its own attribute.
- **The new repo `collage-errortrack`** (stdlib only) works in four steps:
  1. builds a Sentry event in `OnError`: the exception chain, frames parsed from a `PanicError` stack, the privacy-filtered request, the user, and `BeforeSend`;
  2. queues it in a bounded channel;
  3. a single goroutine sends Sentry envelopes, with a per-minute cap and 429 pauses;
  4. Shutdown drains the queue.

**Tech Stack:** Go 1.26, stdlib (net/http, encoding/json, crypto/rand, runtime/debug), collage v0.45.0.

**Spec:** `docs/superpowers/specs/2026-10-04-errortrack-design.md`

## Global Constraints

- Never use the Go type `any` in new code (`PanicError.Value`'s existing annotated `any` is the language's; read it only through `%v`).
- Core change additive only. The one visible change: the request-level panic's error text no longer embeds the stack. The stack moves to a `stack` log attribute and goes in the CHANGELOG.
- Plugin is stdlib only, besides github.com/Elagoht/collage.
- Never log or echo the DSN or its key.
- Never send `Cookie`, `Authorization`, `Proxy-Authorization`, CSRF headers or any header outside the allowlist: `User-Agent`, `Accept`, `Accept-Language`, `Content-Type`, `Content-Length`, `Referer` (with the query removed).
- Defaults:

  | Option | Default |
  |---|---|
  | `MinStatus` | 500 |
  | `SampleRate` | 0 means 1 |
  | `PerMinute` | 60 |
  | `QueueSize` | 100 |
  | `Timeout` | "5s" |
  | `InDevelopment` | false |
  | `Environment` | "development" when `host.DevMode()`, else "production" |
  | `Release` | `host.BuildID()` |
  | `ServerName` | `os.Hostname()` |

- Plugin conventions:
  - module `github.com/Elagoht/collage-errortrack`, with a comment above `module`, `go 1.26`;
  - `const Name = "elagoht/errortrack"`;
  - `New(opts)`; `Version()` returns "0.1.0";
  - README titled `# elagoht/errortrack`; `.gitignore` copied from collage-cdnpurge; no LICENSE.
- Plugin Init is lazy: tests call `app.Start()` (it returns Init errors); Configure errors come from `collage.New`.
- Core work goes in a worktree: `git worktree add -b feat/error-event ../collage-errorevent main`. The plugin builds through a go.work (mktemp dir: `go work init ~/Desktop/collage-errorevent ~/Desktop/collage-errortrack`), and its go.mod requires collage v0.44.0 as a placeholder until release.
- Never `git add -A`. Commit trailer: `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`. Nothing is published inside tasks.

## Review Focus

1. **A panic whose value is itself an error** (`panic(err)`): the chain must show both the PanicError and the wrapped error, with no infinite loop on cyclic `Unwrap`. Test: `TestExceptions_PanicWithError`, plus a depth cap of 10 (Task 3).
2. **Sentry down while errors pour in:** requests must never slow down, and memory must stay bounded. Test: `TestSend_NeverBlocks`, where a blocking fake server and 1,000 errors finish quickly and the queue stays at `QueueSize` or below (Task 4).
3. **A secret in the error message itself:** documented, and `BeforeSend` can scrub it. Test: `TestBeforeSend_Scrubs` (Task 3).
4. **A malformed or empty stack** (a `PanicError` built by hand with garbage `Stack`): no panic, no frames. Test: `TestFrames_Garbage` (Task 3).
5. **Shutdown while a send is in flight to a hanging server:** it returns when its context ends. Test: `TestShutdown_RespectsContext` (Task 4).

---

### Task 1: ErrorEvent.Status/Request, typed request-level panics (core)

**Files:**
- Modify: `internal/plugin/hooks.go` (`ErrorEvent` struct, ~line 497): add the `Status` and `Request` fields with doc comments
- Modify: `internal/httpx/errorpage.go`: `reportError` (~158) fills `Status: f.status, Request: r` and logs a `stack` attribute for a `*render.PanicError`; `reportErrorPageFailure` (~306) fills `Request: r, Status: http.StatusInternalServerError`
- Modify: `internal/httpx/handler.go` (~495): `fmt.Errorf("%w: %w", ErrPanic, &render.PanicError{Value: recovered, Stack: debug.Stack()})`
- Test: `pkg/collage/errorevent_test.go` (new)
- Modify: `CHANGELOG.md` (v0.45.0), `docs/plugins.md` (ErrorHook section)

**Interfaces:**
- Produces:
  - `plugin.ErrorEvent{…; Status int; Request *http.Request}` (also `collage.ErrorEvent`);
  - request-level panics satisfy `errors.Is(err, collage.ErrPanic)` and `errors.As(err, **collage.PanicError)`.

- [ ] **Step 1: Failing tests** `pkg/collage/errorevent_test.go`. A recording `ErrorHook` plugin keeps every event under a mutex, and an app (memory cache off) has:
  - a page whose data handler returns `errors.New("boom")` (expect status 500);
  - a page with a fragment path that fails;
  - an action whose handler returns an error;
  - a document whose handler returns an error;
  - a mount, `app.Mount("/files", fstest.MapFS{})`, requested at a missing file (a 404 that is reported at all: assert what the event says, `Status == 404`);
  - an `app.Handle("/boom", …)` handler that panics with `errors.New("inner")`.

  Assert for each: `ev.Status` equals the status the response had, and `ev.Request != nil && ev.Request.URL.Path == <path>`.

  For the panic, also assert `errors.Is(ev.Err, collage.ErrPanic)`, then `var pe *collage.PanicError; errors.As(ev.Err, &pe)` with `len(pe.Stack) > 0`, and `errors.Is(ev.Err, inner)` (the panic value is reachable). Capture the app's logger (a `slog.NewTextHandler` over a buffer) and assert the panic's log line contains `stack=` and that the `error=` value does not contain "goroutine ".

  For the error page that fails to render, register an error page whose fragment errors and trigger a 500: one event has `Stage == "error-page"` (check the constant's real value in errorpage.go) and `Status == 500`.

- [ ] **Step 2:** Run `go test ./pkg/collage -run ErrorEvent -count=1`. Expected: FAIL to build (`ev.Status` undefined).
- [ ] **Step 3: Implement** as listed under Files. In `reportError`'s two log calls, append `"stack", string(pe.Stack)` when `errors.As(f.err, &pe) && len(pe.Stack) > 0`. Add `internal/render` to `handler.go`'s imports if missing (httpx already imports render for `render.Engine`).
- [ ] **Step 4:** Run the full suite, `-race -short`, vet, staticcheck and gofmt. Run `grep -rn 'goroutine \|ErrPanic' internal pkg --include=*_test.go` and update any test that matched the old message text; list them in the report.
- [ ] **Step 5:** CHANGELOG `## v0.45.0`, in house style (read the v0.44.0 entry first):
  - `### Added`: **ErrorEvent says which status a failure is answered with, and carries the request.**
  - `### Changed`: **a panic recovered while serving a request reaches ErrorHook as a `*collage.PanicError`**. Say that `errors.Is(ErrPanic)` still holds, and that the stack left the message for a `stack` log attribute.
  - `docs/plugins.md`: document the two fields in the ErrorHook section.
- [ ] **Step 6:** Commit `feat: ErrorEvent carries its status and request; a request's panic is a PanicError`.

### Task 2: Module, options, DSN

**Files** (new repo `~/Desktop/collage-errortrack`): `go.mod`, `.gitignore`, `errortrack.go`, `options.go`, `dsn.go`, `options_test.go`, `dsn_test.go`

**Interfaces:**
- Produces:
  - `Name`, `Options`, `User`, `Event`, `Exception`, `Frame`, `RequestInfo`, `Duration`, `New`, `(*Plugin).Name/Version/Configure/Init/Shutdown` (the spec's types and fields verbatim);
  - unexported `type dsn struct{ Key, Host, Scheme, Prefix, Project string }`;
  - `func parseDSN(raw string) (dsn, error)`;
  - `func (d dsn) endpoint() string` returning `scheme://host/<prefix>api/<project>/envelope/`;
  - `func (d dsn) auth(version string) string` returning the `X-Sentry-Auth` value.

- [ ] **Step 1:** `mkdir ~/Desktop/collage-errortrack && cd $_ && git init -q`. Write go.mod by hand: a comment above `module`, then `go 1.26`, then `require github.com/Elagoht/collage v0.44.0` (a placeholder). Create the go.work as in Global Constraints. Copy `.gitignore`.
- [ ] **Step 2: Failing tests.**
  - `dsn_test.go`: table tests for `parseDSN`:
    - `https://abc@o1.ingest.sentry.io/123` gives Key abc, Host o1.ingest.sentry.io, Prefix "", Project 123, and endpoint `https://o1.ingest.sentry.io/api/123/envelope/`;
    - `https://abc@sentry.example.com:9000/prefix/7` gives endpoint `https://sentry.example.com:9000/prefix/api/7/envelope/`;
    - these each fail: no key, no project, a non-numeric project, an ftp scheme, and an empty string. The error text must not contain the key.
    - `auth("0.1.0")` equals `Sentry sentry_version=7, sentry_key=abc, sentry_client=collage-errortrack/0.1.0`.
  - `options_test.go`, start errors through `collage.New` and then `app.Start()`:
    - DSN missing;
    - `DSNEnv` set to an empty variable (`t.Setenv`);
    - an invalid DSN, whose error must not contain the key;
    - `SampleRate` -0.1 or 1.5;
    - a negative `MinStatus`, `PerMinute` or `QueueSize`.

    One valid set starts. Defaults are applied: assert them through an internal test reading `p.opts` (package errortrack) after `Configure`.
- [ ] **Step 3:** Run them. Expected: FAIL.
- [ ] **Step 4: Implement.**
  - **Configure** decodes the config over opts, resolves `DSNEnv` (it wins when set), parses and validates, and applies the defaults. `Release` and `Environment` need the host's BuildID and DevMode; use `ConfigHost.DevMode()`, and fill `Release` in Init from `host.BuildID()` when empty.
  - **Duration** has `UnmarshalJSON` for "5s" or nanoseconds, copied in spirit from collage-tenant/duration.go.
  - **Init** stores the host and logger. When disabled (DevMode && !InDevelopment) it starts nothing; otherwise it starts the sender (Task 4 provides it; stub `startSender()` as a no-op here).
- [ ] **Step 5:** Run `GOWORK=<w> go test -race ./... -count=1`, vet, staticcheck and gofmt. Expected: clean.
- [ ] **Step 6:** Commit `feat: elagoht/errortrack's options and DSN`.

### Task 3: Building an event

**Files:** `event.go`, `frames.go`, `request.go`, `event_test.go`, `frames_test.go` (internal tests)

**Interfaces:**
- Consumes: `Options`, `Event`, etc. (Task 2).
- Produces:
  - `func (p *Plugin) build(ctx context.Context, err error, status int, stage string, r *http.Request) (*Event, bool)`, where false means drop (BeforeSend);
  - `func exceptions(err error) []Exception`;
  - `func parseFrames(stack []byte, mainModule string) []Frame`;
  - `func requestInfo(r *http.Request, pattern string, o Options) *RequestInfo`.

- [ ] **Step 1: Failing tests:**
  - **`TestExceptions_Chain`:** `fmt.Errorf("handler: %w", fmt.Errorf("db: %w", io.EOF))` gives 3 exceptions, outermost last. Their `Type`s are `*fmt.wrapError`, `*fmt.wrapError` and `*errors.errorString`, and each `Value` is that link's own `Error()`.
  - **`TestExceptions_PanicWithError`:** `fmt.Errorf("%w: %w", ErrPanic, &collage.PanicError{Value: inner, Stack: realStack})` includes a `*collage.PanicError` exception with frames, and the inner error.
    - Build `realStack` by recovering a real panic with `debug.Stack()`.
    - A self-cycling error type (whose `Unwrap` returns itself) stops at depth 10.
  - **`TestFrames`:** a real `debug.Stack()` from a test helper parses to frames, each with a non-empty Function and File and Line > 0. The test's own function has `InApp == true` when `mainModule` is `"github.com/Elagoht/collage-errortrack"`; runtime frames are false. Frames are ordered oldest call first (Sentry's order).
  - **`TestFrames_Garbage`:** `parseFrames([]byte("nonsense\n\t"), "")` returns nil with no panic, and an empty stack does too.
  - **`TestRequestInfo`:**
    - a request with Cookie, Authorization, X-CSRF-Token, User-Agent and Referer `https://x/a?q=1`, at `/reset/abc?token=s&page=2`, with pattern `/reset/{token}`;
    - defaults: URL is `scheme://host/reset/{token}`, Query is `page=[filtered]&token=[filtered]` (sorted), Headers hold only User-Agent and Referer `https://x/a`, and IP is "";
    - `SendPath` gives the raw path, `SendQuery` gives the raw query, and `SendIP` gives the RemoteAddr host;
    - no option ever sends Cookie, Authorization or CSRF.
  - **`TestBuild`:**
    - Level is "fatal" for a panic and "error" otherwise;
    - Tags hold stage, method, status and route kind;
    - Transaction is the pattern from `collage.RouteInfo(ctx)` (build ctx with a real collage request through a tiny app, or test via Task 5's integration; an internal unit test may pass the pattern directly through a helper);
    - User comes from the callback, and a panicking callback gives no user;
    - a non-panic with Status 404 is dropped under the default MinStatus, Status 0 is kept;
    - EventID is 32 hex characters, Environment, Release and ServerName are set.
  - **`TestBeforeSend_Scrubs`:** BeforeSend edits `Exceptions[i].Value` to strip "password=…", and the built event reflects it. BeforeSend returning false drops the event, and a panicking BeforeSend drops it and logs the panic.
  - **SampleRate:** an injectable random function `p.rand func() float64` decides keep/drop.
- [ ] **Step 2:** Run them. Expected: FAIL.
- [ ] **Step 3: Implement** per spec "Building an event".
  - `parseFrames` reads debug.Stack's format: a "goroutine N [...]:" header, then pairs of lines, a function line ("pkg.Func(...)") and a tab-indented "file:line +0x.." line.
  - Strip the argument list from function names and drop "created by" lines.
  - Return frames reversed, so the oldest call comes first.
  - `mainModule` comes from `debug.ReadBuildInfo()`, read once; in tests pass it explicitly.
- [ ] **Step 4:** Run `-race`, vet, staticcheck and gofmt. Expected: clean.
- [ ] **Step 5:** Commit `feat: an event with its exception chain, frames and a filtered request`.

### Task 4: Sending

**Files:** `send.go`, `send_test.go` (internal), `faketentry_test.go` (a fake Sentry: `httptest.NewServer` recording envelopes, with knobs for status, a Retry-After header, and a hang channel)

**Interfaces:**
- Consumes: `dsn.endpoint()`, `dsn.auth()`, `Event` (Tasks 2–3).
- Produces:
  - `func (p *Plugin) startSender()`;
  - `func (p *Plugin) enqueue(e *Event)` (never blocks);
  - `func (p *Plugin) drain(ctx context.Context) error`, used by Shutdown;
  - `func envelope(e *Event, now time.Time) ([]byte, error)`, the Sentry JSON with snake_case keys built from typed structs: `event_id`, `timestamp`, `platform:"go"`, `level`, `environment`, `release`, `server_name`, `transaction`, `tags`, `user`, `request{method,url,query_string,headers}`, and `exception{values:[{type,value,stacktrace{frames:[{function,filename,lineno,in_app}]}}]}`;
  - an injectable clock `p.now func() time.Time`.

- [ ] **Step 1: Failing tests:**
  - **`TestEnvelope`:**
    - three lines;
    - the first line has `event_id` and `sent_at`;
    - the second is `{"type":"event","length":N}`, where N is the byte length of line three;
    - line three is valid JSON with every field mapped.
  - **`TestSend_Posts`:** the fake receives a POST to `/api/123/envelope/` with the `X-Sentry-Auth` header and the envelope.
  - **`TestSend_NeverBlocks`:** with the fake hanging, 1,000 `enqueue` calls return within 100ms, the queue length never exceeds `QueueSize`, and the drop counter is ≥ 1000 − QueueSize − 1.
  - **`TestSend_PerMinute`:** `PerMinute: 3`, with a fixed clock, sends 3 of 5 events. Advancing the clock 61s sends again.
  - **`TestSend_RateLimited429`:** a 429 with `Retry-After: 30` makes the next event within 30s (by the clock) go unsent and counted, and sending resumes after.
  - **`TestSend_ErrorLogsNoKey`:**
    - the fake answers 500, the transport errors, and the log is captured;
    - the log contains neither the DSN key nor the full DSN;
    - drops are logged at most once per minute.
  - **`TestShutdown_Drains`:** 5 queued events reach the fake before Shutdown returns.
  - **`TestShutdown_RespectsContext`:** with the fake hanging, Shutdown with a 100ms context returns within ~200ms.
- [ ] **Step 2:** Run them. Expected: FAIL.
- [ ] **Step 3: Implement** per spec "Sending":
  - one goroutine;
  - `chan *Event` with capacity `QueueSize`;
  - `select` with `default` to drop;
  - a sliding minute window of send times;
  - `pausedUntil` set from 429 (Retry-After seconds or HTTP date; `X-Sentry-Rate-Limits`' largest number of seconds);
  - each send uses `context.WithTimeout(Timeout)` and `opts.HTTPClient`, falling back to a client with a 10s timeout;
  - the response body is read up to 64KiB and discarded;
  - Shutdown closes the channel and waits for the goroutine or the context.
  - The log messages never interpolate the dsn struct; log the host only.
- [ ] **Step 4:** Run `-race -count=3`, vet, staticcheck and gofmt. Expected: clean.
- [ ] **Step 5:** Commit `feat: events are queued and sent as Sentry envelopes, never blocking a request`.

### Task 5: OnError, Capture, end to end

**Files:** `errortrack.go` (`OnError`, `Capture`, compile-time `var _ collage.ErrorHook = (*Plugin)(nil)`), `e2e_test.go`

- [ ] **Step 1: Failing tests** (`e2e_test.go`, a real collage app plus the fake Sentry):
  - a page failing with 500 sends one event, with Transaction set to the route pattern;
  - an `app.Handle` panic sends level "fatal" with frames;
  - a missing page (404) sends nothing, and with `MinStatus: 400` it does;
  - in DevMode with `InDevelopment` false nothing is sent and no goroutine is started (check `p.sending == false`, an internal field, or that the fake got nothing after Shutdown);
  - `Capture(context.Background(), errors.New("job failed"))` sends an event with no request;
  - Cookie and Authorization sent by the client never appear in any envelope body (search the raw bytes).
- [ ] **Step 2:** Run them. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - `OnError(ctx, ev)`: `if !p.enabled { return nil }`; `e, ok := p.build(ctx, ev.Err, ev.Status, ev.Stage, ev.Request)`; if ok, enqueue it; always `return nil`.
  - `Capture` calls build with status 0, stage "capture" and a nil request; the transaction still comes from `collage.RouteInfo(ctx)` when ctx carries a route. Status 0 is never filtered by `MinStatus` (an unknown status, as from a failure outside a request, is reported; OnError follows the same rule).
- [ ] **Step 4:** Run `-race`, vet, staticcheck and gofmt. Expected: clean.
- [ ] **Step 5:** Commit `feat: OnError and Capture send to Sentry`.

### Task 6: README and collage.json

- [ ] **Step 1:** README `# elagoht/errortrack` covers:
  - install;
  - config with `dsnEnv`;
  - the options table with defaults;
  - what is sent and what never is (the privacy table);
  - the warning that error messages may carry secrets, with a `BeforeSend` scrub example;
  - the `User` example sending only an id;
  - `Capture` for jobs;
  - GlitchTip and self-hosted Sentry;
  - limits: errors only, no retries, no performance data;
  - that it requires collage v0.45.0.
- [ ] **Step 2:** Regenerate collage.json:

  ```bash
  D=$(mktemp -d)
  ln -s ~/Desktop/collage-errortrack $D/collage-errortrack
  GOWORK=off go run -C ~/Desktop/collage-snippets-highlighter/tools/schemagen . -src $D -manifests -out $D/schema.json
  ```

  Report if a decoder entry is needed for `Duration`; do not edit the highlighter repo.
- [ ] **Step 3:** Commit `docs: README and collage.json`.

### Task 7: Docs site and roadmap

- [ ] **Step 1:** Docs site `~/Desktop/collage-docs`.
  - First check `git status`. If it holds uncommitted changes that are not yours, leave them untouched: do not stash, discard or commit them.
  - Create `docs/v0.45.0` from the branch's current commit (it equals origin/main).
  - EN + TR: the ErrorEvent fields in the plugin-writing guide's ErrorHook section, the PanicError change, and an elagoht/errortrack entry in the plugins page (ops group).
  - Run `go test ./... -count=1` in a clean `git archive HEAD` export, so foreign dirty files do not affect it.
  - Commit only your files: `docs: v0.45.0 ErrorEvent and elagoht/errortrack`.
- [ ] **Step 2:** Roadmap rows (Turkish) in the core worktree's `docs/superpowers/plans/2026-10-03-v1-roadmap.md`:
  - `| errortrack | ErrorEvent.Status ve Request; istek seviyesindeki panic artık PanicError | eklemeli | v0.45.0 |`
  - `| errortrack | A2: RequestHook'un finish(status)'u zaten karşılıyor; bayt sayısı accesslog'la birlikte | karar | — |`

  Commit `docs: the errortrack rows of the core-change log`.

## Release (controller, after the final review)

1. Core: ff-merge feat/error-event into main, run the tests, tag v0.45.0, push, watch the tag CI, and check `go get` works.
2. Plugin:
   1. `GOWORK=off go get collage@v0.45.0`, then `go mod tidy`;
   2. run `GOWORK=off go test -race`;
   3. commit;
   4. `gh repo create Elagoht/collage-errortrack --public --description "A collage plugin that sends server errors and panics to Sentry"`;
   5. push, then tag v0.1.0.
3. Add `collage-errortrack` to the CI matrix in alphabetical order, after collage-devtoolbar or wherever it sorts. Then run a workflow_dispatch and watch it.
4. Docs site: `go get collage@v0.45.0` and commit go.mod/go.sum only. Test in a clean export, then fast-forward push the branch to main without touching foreign dirty files.
5. Clean up the worktree and branches, and update memory.

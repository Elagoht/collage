# elagoht/errortrack: send server errors to Sentry

Date: 2026-10-04
Status: approved design, pending implementation
Roadmap: [v1.0 roadmap](../plans/2026-10-03-v1-roadmap.md), phase 2, the
"errortrack" item (A2)

## Motivation

The roadmap pairs errortrack with A2, a "response completed" hook with the final
status, bytes, route and duration. Exploration found two things.

- **Most of A2 already exists.** `RequestHook.OnRequest` returns a
  `finish(status)` collage calls once the response is written. Route and
  duration are already reachable through `collage.RouteOf` / `RouteInfo` and the
  plugin's own clock. Only the byte count is missing, and only accesslog needs
  it. A2's remainder (bytes, retiring accesslog's writer wrapper) is deferred to
  when accesslog is touched.
- **The real gaps are in `ErrorHook`.** `ErrorEvent` does not say which status
  the failure is answered with, nor carry the request. A panic recovered at the
  request level reaches plugins as text, with the stack inside the message
  (`fmt.Errorf("%w: %v\n%s", ErrPanic, recovered, debug.Stack())`). Render
  panics, by contrast, are already a typed `*collage.PanicError`.

The user wants errors only (5xx and panics), sent through the Sentry protocol.

## Goals

- **Core (collage v0.45.0), additive:**
  - `ErrorEvent.Status` and `ErrorEvent.Request`;
  - request-level panics reported as a `*PanicError` that still wraps `ErrPanic`.
- **A plugin, `elagoht/errortrack` (`github.com/Elagoht/collage-errortrack`):**
  - sends 5xx failures and panics to Sentry, or any Sentry-protocol service
    (GlitchTip, self-hosted);
  - stdlib only;
  - private by default;
  - never blocks a request.

## Non-goals

- Performance monitoring (Sentry transactions), and 4xx reporting.
- A generic sink interface or webhook (Sentry protocol only).
- Retrying failed sends.
- A2's byte count: deferred to the accesslog work.
- Stack traces for non-panic Go errors. They carry none, and capturing the
  reporting goroutine's stack would show collage, not the cause.

## Core (collage v0.45.0)

### ErrorEvent

```go
type ErrorEvent struct {
	Err   error
	Page  *types.Page
	Path  string
	Stage string
	// Status is the status the failure is answered with, or 0 when unknown.
	Status int
	// Request is the reader's request, for reading only; its body may already
	// have been read. Nil when the failure did not come from a request.
	Request *http.Request
}
```

`reportError` (internal/httpx/errorpage.go) fills both from the failure and
`r`. `reportErrorPageFailure`, where the error page itself failed to render,
fills `Request` and sets `Status` to 500.

### Panics

The request-level recover in internal/httpx/handler.go reports this:

```go
fmt.Errorf("%w: %w", ErrPanic, &render.PanicError{Value: recovered, Stack: debug.Stack()})
```

- `errors.Is(err, collage.ErrPanic)` still holds.
- `errors.As(err, &pe)` reaches `Value` and `Stack`.
- The stack is no longer part of the message. `reportError`'s log line adds a
  separate `stack` attribute when the error holds a `*PanicError` with a stack,
  so logs keep it.
- The development error page already prints a `PanicError`'s stack.
- The CHANGELOG notes the changed message text.

## The plugin

### Options

```go
const Name = "elagoht/errortrack"

type Options struct {
	DSNEnv        string   `json:"dsnEnv"`        // name of the environment variable holding the DSN
	DSN           string   `json:"-"`             // or set from Go
	Environment   string   `json:"environment"`   // default "development" in DevMode, else "production"
	Release       string   `json:"release"`       // default host.BuildID()
	MinStatus     int      `json:"minStatus"`     // default 500; panics are always sent
	SampleRate    float64  `json:"sampleRate"`    // fraction sent, (0, 1]; 0 (unset) means 1
	PerMinute     int      `json:"perMinute"`     // events sent per minute at most, default 60
	QueueSize     int      `json:"queueSize"`     // default 100
	InDevelopment bool     `json:"inDevelopment"` // default false: nothing is sent in development
	Timeout       Duration `json:"timeout"`       // per send, default "5s"
	SendPath      bool     `json:"sendPath"`      // raw path instead of the route pattern
	SendQuery     bool     `json:"sendQuery"`     // query values instead of "[filtered]"
	SendIP        bool     `json:"sendIP"`        // the client address

	User       func(r *http.Request) User `json:"-"`
	BeforeSend func(e *Event) bool        `json:"-"` // false drops the event
	HTTPClient *http.Client               `json:"-"`
}

type User struct{ ID, Username, Email string }

// Event is what is sent, in the shape the plugin builds and BeforeSend may edit.
type Event struct {
	EventID     string
	Timestamp   time.Time
	Level       string // "error", or "fatal" for a panic
	Exceptions  []Exception // outermost last, as Sentry expects
	Tags        map[string]string
	Request     *RequestInfo
	User        *User
	Environment string
	Release     string
	ServerName  string // os.Hostname(), or "" when it fails
	Transaction string // the route pattern
}

type Exception struct {
	Type, Value string
	Frames      []Frame // only for a panic
}

type Frame struct {
	Function, File string
	Line           int
	InApp          bool
}

type RequestInfo struct {
	Method, URL, Query string
	Headers            map[string]string
	IP                 string
}

func New(opts Options) *Plugin
func (p *Plugin) Capture(ctx context.Context, err error)
```

`Duration` is the plugin's own `time.Duration`, read from JSON as a string like
"5s", following the convention of cdnpurge and tenant.

### Building an event (OnError)

1. Nothing is sent in development without `InDevelopment`; an error with a
   known status (`Status != 0`) below `MinStatus` that is not a panic is
   skipped; otherwise the event is kept with probability `SampleRate`. An
   unknown status (a failure outside a request) is reported.
2. **Exceptions:** walk the error chain with `errors.Unwrap`, following, in a
   multi-error, the first branch that holds a `*PanicError`, else the first
   branch (in `fmt.Errorf("%w: %w", ErrPanic, pe)` the first branch is the bare
   `ErrPanic`), at most 10 links deep. Each link becomes one exception, outermost last, with
   its `%T` type and its own message. A `*PanicError` link gets frames parsed
   from its `Stack` (runtime/debug format: function line, then a file:line line).
   `InApp` is true for functions in the main module, from
   `debug.ReadBuildInfo().Main.Path`, and false for the standard library and
   dependencies.
3. **Level:** "fatal" for a panic, "error" otherwise.
4. **Tags:** stage, route kind, method, status. **ServerName:** `os.Hostname()`,
   read once at Init.
5. **Transaction:** the route pattern from `RouteInfo`.
6. **Request** (see Privacy).
7. **User:** from `User(r)` when set. A panic in it is recovered, and the event is
   sent without a user.
8. **BeforeSend** runs last. False or a panic drops the event; a panic is logged.

`Capture(ctx, err)` builds the same event without a request: the core keeps no
request in `ctx`. When `ctx` carries a route (`RouteInfo`), its pattern becomes the
transaction. `MinStatus` does not apply to an explicit capture.

### Sending

- The DSN has the form `https://<key>@<host>[:port]/<project>`, with an optional
  path prefix before the project. It is parsed at Configure.
- Each event is POSTed to `https://<host>/<prefix>api/<project>/envelope/`:
  - header `X-Sentry-Auth: Sentry sentry_version=7, sentry_key=<key>, sentry_client=collage-errortrack/<version>`;
  - an envelope body: a header line `{"event_id","sent_at"}`, an item line
    `{"type":"event","length":N}`, then the event JSON.
- **Queue:** events go into a bounded queue (`QueueSize`), drained by one
  goroutine. A full queue drops the event and counts it.
- **Rate limit:** at most `PerMinute` events are sent per rolling minute; more are
  dropped and counted.
- **Errors:** a non-2xx answer or a transport error drops the event and logs it.
  The log never contains the DSN or its key.
- **429:** `Retry-After`, or the longest `X-Sentry-Rate-Limits` window, pauses
  sending; events in the pause are dropped and counted.
- **Counting:** dropped counts are logged at most once a minute.
- **Shutdown:** stop accepting events, then drain the queue until the shutdown
  context ends; what is left is dropped.

### Privacy

| Data | Default | Opt in |
|---|---|---|
| Headers | Only `User-Agent`, `Accept`, `Accept-Language`, `Content-Type`, `Content-Length` and `Referer` (reduced to its origin, `scheme://host`, unless `SendPath`; with it, credentials, query and fragment removed). `Cookie`, `Authorization`, `Proxy-Authorization`, CSRF tokens and every other header are never sent. | `BeforeSend` can add to `Headers` |
| URL path | The route pattern (`/reset/{token}`); when no route resolved (a middleware's panic), only `scheme://host` | `SendPath` |
| Query | Keys, each value `[filtered]` | `SendQuery` |
| Body | Never | — |
| Client IP | Not sent | `SendIP` (RemoteAddr host) |
| User | Not sent | `User` callback |
| Error message, panic value | Sent; the README warns that messages may carry secrets (a database DSN) or the request path (collage's own messages name it) and shows `BeforeSend` scrubbing | — |
| Stack | Functions, files and lines; never values | — |

### Errors

| Case | Behaviour |
|---|---|
| DSN missing or invalid | Start error. The error never echoes the key. In development without `InDevelopment` a missing DSN is not an error (nothing is sent); a DSN that is given is still validated. |
| `DSNEnv` names an empty variable | Start error. |
| `SampleRate` negative or above 1, or a negative MinStatus, PerMinute or QueueSize | Start error. |
| Development without `InDevelopment` | No queue, no goroutine, nothing sent. |

## Testing

**Core:**

- `Status` and `Request` are set for a failing page, fragment path, action,
  document, mount and panic, and for an error page that fails to render
  (status 500).
- A request-level panic reaches `ErrorHook` as `errors.Is(ErrPanic)` and
  `errors.As(*PanicError)`, with a non-empty stack, and the log line has a
  `stack` attribute.
- Existing tests pass, and the compatibility run over all plugins' latest tags
  passes.

**errortrack**, with a fake Sentry from `httptest` (stdlib only):

- A 500 sends one envelope.
  - Its auth header carries the key and the version.
  - The event has the exception chain, outermost last, and the transaction is
    the route pattern.
- A panic sends level "fatal". Its frames have function, file and line, and
  `InApp` is true for the test's own package.
- A 404 sends nothing; with `MinStatus: 400` it does.
- Privacy:
  - Cookie, Authorization and X-CSRF-Token never appear in the body sent.
  - The route pattern is used instead of the path, query values are filtered,
    and the IP is absent.
  - Each opt-in flag flips its own case.
- `User` sets the user, and a panicking `User` sends the event without one.
- `BeforeSend` can edit the event and can drop it; a panic in it drops the event.
- Limits:
  - 429 with Retry-After pauses sending;
  - a full queue drops events;
  - `PerMinute` is enforced (with an injectable clock);
  - Shutdown drains the queue.
- Development sends nothing.
- `Capture` sends an event without a request.
- No log line contains the DSN key.
- Start errors, one per case.

## Release order

1. collage v0.45.0, with the CHANGELOG (ErrorEvent fields, PanicError for
   request-level panics, the changed message text) and the docs.
2. The new public repo `Elagoht/collage-errortrack` v0.1.0, on collage v0.45.0.
3. Add it to collage's CI matrix, then run a workflow_dispatch.
4. Docs site, EN and TR: the plugin, and the ErrorEvent fields in the
   plugin-writing guide.
5. Roadmap core-change log:
   - "errortrack → ErrorEvent.Status/Request, PanicError for request panics,
     additive, v0.45.0";
   - "A2: `RequestHook` finish(status) already covers it; bytes deferred to
     accesslog".

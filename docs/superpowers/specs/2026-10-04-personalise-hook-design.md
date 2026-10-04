# PersonaliseHook: per-response rewriting after the cache

Date: 2026-10-04
Status: approved design, pending implementation
Roadmap: [v1.0 roadmap](../plans/2026-10-03-v1-roadmap.md), phase 1, the
"csp-nonce" item

## Motivation

The roadmap planned a csp-nonce plugin to answer one question: does the core need
a per-response stage after the cache, given that a CSP nonce must be new on every
response and collage serves one cached render to many readers?

Exploration answered it:

- **elagoht/secure already does per-response nonces.** `{{cspNonce}}` renders a
  marker that is random per process. A middleware buffers every `text/html`
  response, replaces the marker with a fresh nonce, writes the same nonce into the
  CSP header, and drops the validators (`ETag`, `Last-Modified`) with
  `Cache-Control: no-store`.
- **The buffering middleware breaks with compression.** When secure is listed
  before elagoht/compress in `Config.Plugins`, secure is the outer middleware and
  sees gzip bytes. It cannot find the marker, so:
  - the marker reaches the browser;
  - the CSP nonce never matches, and every inline script is blocked;
  - the page keeps its ETag and a public Cache-Control.

  The other order works. Nothing warns about it. This was reproduced with a probe
  in both orders.
- **The core already has the needed stage, closed to plugins.**
  `Handler.personalise` (`internal/httpx/handler.go`) puts each reader's CSRF token
  into the shared body. It runs inside every middleware, so before any
  compression. It is the single chokepoint for all six HTML write paths. It
  already handles a body that became per-reader: it recomputes the ETag and sets
  `private, no-store`.

So the core does not need a new stage. It needs to open the existing one. A
separate csp-nonce plugin would duplicate secure.

## Goals

- An optional plugin hook, `PersonaliseHook`, that runs inside `personalise` on
  every HTML response collage writes from a render, cached or not.
- elagoht/secure v0.2.0 moves its nonce from the buffering middleware to the hook.
  - The ordering bug with compress disappears.
  - The buffering, the hand-written Content-Length and the Flush pass-through go
    with it.
- Additive: `plugin.Host` gains no method, no signature changes, and a site with
  no `PersonaliseHook` plugin sends byte-identical responses.

## Non-goals

- A hash-based CSP mode (`'sha256-…'` for inline scripts, keeping pages
  cacheable). It is a possible later secure feature.
- Documents (non-HTML responses). They carry no CSRF marker today and get no hook.
- A new csp-nonce plugin.
- The roadmap's A2 "response completed" hook. That is a different, later stage
  (after the write, with the final status and bytes).

## Core (collage v0.43.0)

### The hook

```go
// internal/plugin/hooks.go; aliased as collage.PersonaliseHook and
// collage.PersonaliseEvent
type PersonaliseHook interface {
	// OnPersonalise is called for each HTML response collage writes from a
	// render — a page (from the cache or fresh), a fragment path or fragment
	// read, an action's HTML answer, an error page — after the response's
	// shared body has been taken from the cache and the reader's forgery token
	// put in, and before any middleware sees the body.
	OnPersonalise(ctx context.Context, ev *PersonaliseEvent) error
}

type PersonaliseEvent struct {
	// Request is this reader's own request, not a shared render's stripped one.
	Request *http.Request
	// Header is the response's header, to set what goes with the body (a CSP
	// carrying the same nonce).
	Header http.Header
	// Body is what will be written. A hook may replace it; the cached copy is
	// never changed.
	Body []byte
	// Personal is set by a hook that made Body particular to this reader. The
	// response is then treated as the forgery token makes it: the ETag is
	// recomputed from the body sent, and a page is "private, no-store".
	Personal bool
}
```

### Where it runs

- Inside `Handler.personalise`, after the CSRF substitution, in plugin
  registration order. Each hook sees the previous hook's `Body`.
- `personalise` returns the hooks' final body. Its `personal` result is
  `csrfPersonal || ev.Personal`.
- Before the dev overlay and the dev reload script, as today for CSRF.
- The six call sites are unchanged and get the hook for free:
  - a fresh page;
  - a cached page (`serveCached`, including a 304 computed against the
    personalised ETag);
  - `writeActionHTML` (action HTML answers and fragment paths served as actions);
  - `writeFragmentRead`;
  - `writeErrorResponse`.
- The registry gets `(*Registry).Personalise(ctx, ev) error`. It is built like
  the other dispatchers: stop at the first failure, with a contained panic
  counted as a failure.

### Errors

| Case | Behaviour |
|---|---|
| A hook errors or panics on a page, fragment or action | Logged. The response becomes a 500 through the existing failure path (`serveFailure` and the error page). |
| A hook errors or panics while writing an error page | Logged. The response is the core's built-in page for that status (`builtinPage`: a title and one sentence, no layout, so no plugin content and no marker). It never recurses into another error page, and it never sends a half-personalised body: a body still carrying a plugin's marker would leak it, and its inline scripts would be blocked anyway. |

`personalise`'s signature gains an error return, or an equivalent internal path,
so callers can route a failure. This is internal to `internal/httpx`, so nothing
exported changes.

### Personal responses

When `Personal` is true, the core does exactly what it does for a forgery token
today:

- the ETag is recomputed from the body sent;
- a page answers `private, no-store`;
- a fragment read keeps `private, no-cache` with the recomputed ETag (a body that
  changes each time simply never matches);
- error and action responses are already `no-store`.

## elagoht/secure v0.2.0

- **Unchanged:**
  - `{{cspNonce}}` and the per-process random marker;
  - the static headers;
  - `Report-Only` in development;
  - removing the marker from a static render in `OnAfterRender`;
  - deleting `If-None-Match: *` in the middleware;
  - the exported API.
- **Middleware:**
  - sets the static headers;
  - when `CSP` is set, makes a nonce, writes the CSP header with it, and puts the
    nonce in the request context under the plugin's own key.

  `nonceWriter` and its buffering are removed. The middleware no longer wraps the
  ResponseWriter.
- **`OnPersonalise`:** when `ev.Body` contains the marker:
  1. take the nonce from `ev.Request.Context()`; if there is none, make one and
     rewrite the CSP header with it;
  2. replace the marker with the nonce;
  3. set `ev.Personal = true`.

  The core drops the ETag and sets no-store.
- **Requirements:**
  - requires collage v0.43.0, because `PersonaliseHook` must exist;
  - `Version()` is "0.2.0";
  - README and collage.json are updated (only descriptions).

## Testing

**Core:**

- The hook is called on each of the six paths. The cached-page case is checked on
  a second request, a real cache hit.
- It runs after the CSRF substitution: it sees the token, never the marker.
- With `Personal` set:
  - a page gets `private, no-store` and an ETag of the sent body;
  - a 304 is never given for a body that differs.
- Hooks run in registration order and each sees the previous body.
- A hook error gives a 500. A hook error on an error page writes the built-in
  page for that status, without recursion and without the marker.
- With no hook, responses are byte-identical; existing tests stay green.
- The plugin compatibility workspace (all 36 plugins, latest tags) passes.

**secure:**

- Both orders with elagoht/compress: the gzip body's nonce equals the header's
  nonce. This is the probe, kept as a regression test with compress as a test
  dependency.
- Two requests for a cached page get two different nonces, each matching its own
  header, with no ETag and `no-store`.
- A fragment path carrying `{{cspNonce}}` is filled.
- A HEAD request works, and `If-None-Match: *` is still dropped.
- A static build has the marker removed.
- No CSP configured, or a policy without `{nonce}`: the nonce attribute and the
  marker are removed, as a static render removes them, and `Personal` stays false,
  so the page keeps a stable ETag and stays cacheable.
- A policy set but no nonce in the request (answered outside secure's middleware,
  say by a plugin listed before it): the hook makes the nonce, writes the header
  and sets `Personal`; the body's nonce equals the header's.

## Release order

1. collage v0.43.0 with CHANGELOG, `docs/plugins.md` (the hook) and
   `docs/caching.md` (personal responses).
2. elagoht/secure v0.2.0, on collage v0.43.0.
3. Docs site, EN and TR: the plugin-writing guide (the hook) and secure's entry
   (the compress ordering fix, nonces filled after the cache).
4. Roadmap core-change log:
   - "csp-nonce → `PersonaliseHook`, additive, v0.43.0; no separate plugin";
   - "(found) secure's nonce broke when listed before compress, fixed in secure
     v0.2.0".

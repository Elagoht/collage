# Health and drain: readiness, a graceful drain on shutdown, and optional load shedding

Date: 2026-10-07
Status: approved design, pending implementation
Roadmap: [v1.0 roadmap](../plans/2026-10-03-v1-roadmap.md), phase 2, "health + drain"
("readiness false → drain traffic → `StreamCloser` → plugin `Shutdown`; the open
'no load shedding' item is decided here").

## Motivation

On SIGTERM, `ListenAndServe` calls `Shutdown`, which closes the dev and plugin
streams, then `server.Shutdown` — which closes the listener at once — then the
plugins. A load balancer that has not yet noticed the instance is going away
keeps sending it requests, and they are refused. There is no liveness or
readiness endpoint, and no way to refuse work under overload.

It has to serve two setups: a single instance (systemd or Docker behind
nginx/Caddy/Cloudflare — the author's own site) and several instances behind a
load balancer or Kubernetes. Defaults suit the single instance.

## Decisions

- **The core owns the shutdown order; everything else is a plugin.** The core
  gains only what a plugin cannot do: a drain phase before the server stops.
  Endpoints, checks and load shedding are the new plugin `elagoht/health`.
- `DrainDelay` defaults to 0: today's behaviour, unchanged.
- `Host` does not grow. A plugin learns of the drain through an optional hook.

## Design

### 1. Core (collage v0.53.0)

`ServerConfig.DrainDelay time.Duration`, beside `ShutdownTimeout`, default 0.

Shutdown, whether from SIGTERM/SIGINT in `ListenAndServe` or from
`App.Shutdown(ctx)`:

1. **Drain starts.**
   - Every plugin implementing `DrainHook` gets `OnDrain()`, once.
   - `server.SetKeepAlivesEnabled(false)`, so each kept-alive connection closes
     after its next response, and its client reconnects through the balancer.
   - The listener stays open and requests are served as normal.
2. **Wait `DrainDelay`.** The wait ends early on a second SIGINT/SIGTERM (someone
   pressed Ctrl-C twice) or when the `Shutdown` ctx is done.
3. **Today's sequence:** dev and plugin streams closed, `server.Shutdown`, then
   the plugins' `Shutdown`. In `ListenAndServe`, `ShutdownTimeout` starts here:
   the drain does not eat into it, so the longest stop is
   `DrainDelay + ShutdownTimeout`.
4. **In development mode** (`App.DevMode()`), `DrainDelay` is ignored, so
   restarts stay instant. `OnDrain` still fires.

```go
// DrainHook is implemented by a plugin that must know the server is about to
// stop taking traffic: readiness turns false, new work is refused. OnDrain is
// called once, when the drain starts, before DrainDelay; it must not block.
type DrainHook interface {
	OnDrain()
}
```

`Shutdown` stays idempotent: the drain runs once, inside the same `sync.Once`.
A `Shutdown` called before `ListenAndServe` serves calls `OnDrain` and skips the
wait, since there is no traffic to drain. At startup, a `DrainDelay` > 0 with
`ShutdownTimeout` 0 logs a warning.

### 2. Plugin `elagoht/health` (new repo Elagoht/collage-health, v0.1.0)

Configuration (`plugins-config.json` or `NewWith`, read with
`collage.PluginConfig`):

```json
{ "elagoht/health": {
    "livePath": "/healthz", "readyPath": "/readyz",
    "checkTimeout": "2s", "cacheFor": "1s",
    "details": false, "maxInFlight": 0 } }
```

**Endpoints.** These are served by its middleware, not as pages or documents, so
a static export never contains them. They answer GET and HEAD only, with
`Cache-Control: no-store`.

- `livePath` (liveness): 200 whenever the process serves a request. It runs no
  checks, because restarting a process whose database is down does not help and
  only adds load.
- `readyPath` (readiness): 503 while draining or while any check fails, else 200.

**Checks.**

```go
h := health.New()
h.Check("db", func(ctx context.Context) error { return db.PingContext(ctx) })
```

- Checks run in parallel, each under `checkTimeout`.
- One result is reused for `cacheFor`, and concurrent probes share one run, so
  any number of probes costs at most one ping per `cacheFor`.
- A check that panics or times out counts as failing; the panic or error is
  logged, never answered. A timed-out check's ctx is cancelled and its late
  result is discarded.
- `Check` after `Init`, an empty name, or a duplicate name panics (a programming
  error, at the line that made it).

**Body.**

- By default the body is plain text: `ok`, `unavailable` or `draining`.
- With `details: true` the body is JSON, for example
  `{"status":"unavailable","checks":{"cache":"ok","db":"failing"}}`, with names
  sorted. An error's text never appears: it can hold a DSN or an internal address.

**Load shedding (opt-in).** `maxInFlight` > 0 caps concurrent requests. A request
over the cap gets 503 with `Retry-After: 1` at once, with no queue.

- The plugin's own endpoints are neither counted nor refused, so probes answer
  under overload.
- `collage.IsCapture` requests are not counted.
- There is one global number, with no per-route limits.

**Drain.** The plugin implements `DrainHook`: `OnDrain` sets a flag, and
`readyPath` answers 503 `draining` from then on.

**Validation at `Init`.** These are errors: `livePath` equal to `readyPath`; a
path not starting with `/`; a path a registered page or document also claims
(the middleware runs before routing, so that page could never be reached);
negative durations or `maxInFlight`.

**Order.** The README says to list `elagoht/health` first among the plugins, so
basicauth, ratelimit or fail2ban never stand between a probe and its answer.

## Testing

**Core**
- During the drain the server still serves.
- Responses carry `Connection: close`.
- `OnDrain` runs once, the wait happens, and a second signal or a done ctx cuts
  the wait short.
- `DrainDelay` 0 behaves exactly as today.
- Development mode skips the wait.
- `Shutdown` before `ListenAndServe` calls `OnDrain` without waiting.
- Timed tests use short durations and drive `Shutdown(ctx)`, not signals.

**Plugin**
- Liveness runs no checks.
- Readiness is 503 for a failing check, a timeout, a panic, and while draining.
- Concurrent probes share one run, and `cacheFor` is honoured.
- `details` never leaks an error's text.
- `maxInFlight` answers 503 at the cap, exempts the endpoints, and does not count
  captures.
- A static export contains no endpoint.
- Every case in the `Init` validation list is refused.
- End to end, with a real listener: `Shutdown(ctx)` turns `/readyz` to 503 while
  a page is still served, then the port closes after the delay.

## Release

1. collage **v0.53.0** (additive): `ServerConfig.DrainDelay`, `DrainHook`.
2. **Elagoht/collage-health v0.1.0**, then the CI-matrix commit, then the matrix
   run from main.
3. Docs:
   - framework `docs/deployment.md`, with systemd (`TimeoutStopSec`) and
     Kubernetes (`terminationGracePeriodSeconds` > `DrainDelay +
     ShutdownTimeout`, and readiness/liveness probes) examples;
   - the docs site EN+TR;
   - the extension's schema for `elagoht/health`.

## Out of scope

A startup endpoint; dependency graphs; non-critical or weighted checks;
per-route load shedding or queueing; health metrics for Prometheus; a public
`Draining()` query on `Host` or `App`.

# Tenants: one site, a host per customer

Date: 2026-10-03
Status: approved design, pending implementation
Roadmap: [v1.0 roadmap](../plans/2026-10-03-v1-roadmap.md), phase 1, first plugin

## Motivation

A SaaS built on collage serves one site to many customers, each on a host of its
own: `acme.app.com`, `globex.app.com`, or a customer's own domain. The pages are
the same. The data, and the URL set, differ per customer: acme has 3 posts and
globex has 50.

Most of this works on today's core:

- the cache key holds the host;
- `collage.Vary` and `collage.Varied` carry a per-request dimension into a shared
  render safely;
- the dev proxy accepts `*.localhost`.

The gap is absolute URLs. `Config.BaseURL` is one value. `Host.BaseURL()` returns
it. The SEO plugins (sitemap, feed, meta, ogimage, indexnow, cdnpurge) read it once
in `Init` and refuse to start without it. A tenant site has no single origin, so
none of them can run. `CacheInvalidateEvent` also carries only paths, not the host
whose entries were dropped. That leaves indexnow and cdnpurge unable to tell which
origin a dropped `/blog/x` belonged to.

The roadmap's question for this plugin is what the core must change for it, and
whether that change is additive or breaking. The answer here is additive.

## Goals

- A plugin, `elagoht/tenant` (`github.com/Elagoht/collage-tenant`), that resolves a
  request's host to a tenant. It takes a static list from `collage.json`, a Go
  `Resolver`, or both.
- Per-tenant data, per-tenant URL sets (sitemap, feed) and per-tenant origins in
  every absolute URL the ecosystem produces.
- Core changes that are additive only:
  - no new `plugin.Host` method;
  - no signature change;
  - no default behaviour change for a site without a resolver.
- Single-origin sites behave exactly as before, including the early `ErrNoBaseURL`
  at `Init`.

## Non-goals

- Different page sets per host (virtual hosts). Routing stays host-independent.
- A domain per locale (`example.com.tr` → `tr`).
- Per-tenant static export (`collage build --host`). It is recorded as a candidate
  in the roadmap's core-change log.
- Tenant data beyond `ID` and `Origin`. The application keeps its own tenant
  records and looks them up by ID.
- A vary dimension that stays out of the response's `Vary` header. This is noted as
  an observation, not done here.

## Prerequisite (done)

v0.41.1: a cacheable document's shared render now strips per-reader context
values, as a page's has since v0.39.0. Sitemaps and feeds are documents, so tenant
isolation depended on it.

## Core changes (collage v0.42.0)

### `OriginResolver`, an optional plugin hook

```go
// in internal/plugin, aliased in pkg/collage
type OriginResolver interface {
	// Origin returns the public origin, "https://acme.app.com", that URLs for
	// host are absolute against, and whether this plugin knows host.
	Origin(ctx context.Context, host string) (origin string, ok bool)
}
```

- The host is the request's (`r.Host`) at render time, and a dropped entry's host
  at invalidation.
- When several plugins implement it, the first in registration order that returns
  `ok` wins.
- The returned origin is validated with `Config.BaseURL`'s rule: a bare
  `scheme://host[:port]`, normalized without a trailing slash. An invalid origin
  counts as `ok == false`, and dev mode logs it once per plugin and origin.
- With no resolver, or none that knows the host, the origin is `Config.BaseURL`,
  which may be `""`.

### `Origins`, an optional `Host` capability

```go
type Origins interface {
	// OriginFor is the origin for host: a resolver's, else Config.BaseURL.
	OriginFor(ctx context.Context, host string) string
	// Dynamic reports whether an OriginResolver is registered, so the origin
	// may vary by host.
	Dynamic() bool
}
```

- The `plugin.Host` and `plugin.ConfigHost` views implement it. A plugin reaches it
  by type assertion: `host.(collage.Origins)`.
- `plugin.Host` gains no method, so existing test doubles still compile. This is
  the first trial of the roadmap's phase 5 pattern: optional capabilities instead
  of a growing `Host`.

### `collage.ParseOrigin(raw string) (string, error)`

- The rule `Config.BaseURL` is validated with, exported: a bare
  `scheme://host[:port]`, scheme `http` or `https`, returned normalized without a
  trailing slash. tenant validates its origins with the same rule core uses.
- `collage.ErrInvalidBaseURL` is now the same value as `plugin.ErrInvalidOrigin`,
  the error it returns; the message is unchanged and `errors.Is` still matches.

### `collage.BaseURL(rc *RenderContext) string`

- Application code reads the request's origin with it: the resolver's origin for
  `rc.Request.Host`, else `Config.BaseURL`.
- It works in a shared render: the host survives `sharedRequest`, and the resolver
  chain is reached through a framework context key that `renderSafe` keeps.
- In a static build (no request) it returns `Config.BaseURL`.

### `collage.VariedContext(ctx context.Context, header string) (string, bool)`

- `Varied` for code that holds only a context: `StaticParams(ctx, locale)`, a
  sitemap's `LastMod(ctx, …)`, a document handler's `ctx`.
- The vary set is a framework key, so it is present in a stripped context.

### `CacheInvalidateEvent.Entries`

```go
type InvalidatedEntry struct {
	Host string // lower-cased, as in the cache key
	Path string
}
// CacheInvalidateEvent gains:
Entries []InvalidatedEntry // sorted by Host, then Path
```

- The tracker records the host beside the path tag, so the host is known for every
  entry an invalidation drops.
- The dependency tracker is in-memory only (`internal/dependency.MemoryTracker`), so
  nothing about invalidation survives a restart. The host is recorded as a
  `collage:host:<host>` tag beside the existing `collage:path:<path>` tag.
- Host tags are exempt from the tracker's `MaxKeysPerTag`, so one busy host cannot
  evict its own entries' hosts; `MaxKeys` still bounds memory.
- `Paths` stays as it is: the deduplicated paths. Whether v1 keeps it is decided in
  phase 5.

## The tenant plugin

```go
const Name = "elagoht/tenant"

type Tenant struct {
	ID     string // stable key: the vary value, and what the app looks up by
	Origin string // "https://acme.app.com"
}

type Static struct {
	ID     string   `json:"id"`
	Origin string   `json:"origin"`
	Hosts  []string `json:"hosts"`
}

// Resolver maps a host to a tenant. ok == false: host is no tenant.
type Resolver func(ctx context.Context, host string) (t Tenant, ok bool, err error)

type Options struct {
	Tenants  []Static `json:"tenants"`
	Resolve  Resolver `json:"-"`
	Bypass   []string `json:"bypass"`   // hosts served without a tenant
	TTL      Duration `json:"ttl"`      // resolver result cache, default 1m
	MaxHosts int      `json:"maxHosts"` // cached hosts, default 10000
}

func New() *Plugin
func NewWith(opts Options) *Plugin
func Version() string

// Duration is the plugin's own time.Duration with a JSON string form ("1m"),
// as in cdnpurge, live, ogimage and opti-image.
type Duration time.Duration

func ID(rc *collage.RenderContext) (string, bool)
func IDFromContext(ctx context.Context) (string, bool)
func (p *Plugin) Lookup(id string) (Tenant, bool)
```

The plugin implements `OriginResolver`: it maps a host to its tenant's `Origin`
from the resolved-host cache, resolving on a miss.

### Request flow (middleware)

1. Delete any client-sent `X-Collage-Tenant` request header.
2. Normalize `r.Host`: lower-case it, drop the port, drop a trailing dot. IDN names
   are left as they are.
3. If the host is in `Bypass`, pass through without a tenant.
4. Otherwise look in the static list, then the cache, then `Resolve`. Cache the
   result, positive or negative.
5. If a tenant is found, call `collage.Vary(r, "X-Collage-Tenant", t.ID)` and pass
   the request on.
6. If no tenant is found, answer `host.ServeStatus(w, r, 404)`.

`ID` and `IDFromContext` read the vary value through `Varied` and `VariedContext`.
They never read a context value of the plugin's own, because a shared render
strips those.

### Errors and edges

| Case | Behaviour |
|---|---|
| Unknown host, empty host | 404 through `ServeStatus`. Negative-cached. |
| `Resolve` errors or panics | 503 with `Retry-After`. The error is logged by the plugin: only core dispatches `ErrorHook`, so it does not reach it. Not cached. |
| `Resolve` returns an invalid `Origin` | 503 and a log entry. It never falls back to `Config.BaseURL`, which would emit absolute links to the wrong domain. |
| Many distinct hosts | The cache is bounded by `MaxHosts`, FIFO, negatives included. Random hosts cannot grow memory or hammer the resolver unboundedly. |
| Static list: a host under two tenants, one ID with two origins, a host both in `Bypass` and under a tenant, an invalid origin | `Init` error, one per case. |
| Static build | No host. `ID` reports false, and `BaseURL` is `Config.BaseURL`. `BuildFinishedHook` warns that the build rendered without a tenant. |
| Actions, `app.Handle` | Per-reader. `IDFromContext(r.Context())` works directly. |
| Development | `acme.localhost:3000` works through the dev proxy. The user lists `acme.localhost` among the hosts. |

## Plugin updates

The rule in every plugin is the same:

- A plugin's own `BaseURL` option, when set, wins, so single-origin setups are
  unchanged.
- Otherwise the origin comes from `Origins.OriginFor` at render or invalidation
  time.
- `ErrNoBaseURL` at `Init` remains when there is no own `BaseURL`, no
  `Config.BaseURL` and `Origins.Dynamic()` is false.

| Plugin | Change |
|---|---|
| sitemap, feed | The origin is resolved per render from `rc.Request.Host`. URL sets already come through `PageURLs(ctx)` → `StaticParams(ctx)`, where the app reads the tenant with `tenant.IDFromContext`. |
| meta | canonical, og:url and og:image are made absolute per render. |
| ogimage | `OriginFor` replaces the stored `p.baseURL`. The dev request-origin behaviour stays. |
| indexnow | `Entries` are grouped by origin, and each origin is submitted in a batch of its own (IndexNow requires one host per batch). The key file is already served on every host. |
| cdnpurge | `Entries` are grouped by origin. The plugin's own `BaseURL` (the CDN origin), when set, is used as today. |
| robots | It also accepts a sitemap given as a path (`/sitemap.xml`), made absolute per request. |

Each gets a minor version bump and requires collage v0.42.0. jsonld builds no
absolute URLs and is unchanged.

## Testing

**Core:**

- `OriginResolver`:
  - first-ok-wins;
  - an invalid origin is rejected, with the dev log;
  - fallback to `Config.BaseURL`.
- `collage.BaseURL`:
  - inside a shared render;
  - two hosts on one path each get their own origin.
- `Origins`:
  - satisfied by both host views;
  - `Dynamic()` is true with a resolver registered;
  - `fakeHost` compiles unchanged.
- `VariedContext`: works in a stripped context, in `StaticParams`, and in a
  document render.
- `CacheInvalidateEvent.Entries`:
  - one path cached under two hosts gives two entries;
  - `Paths` is unchanged;
  - a host tag is exempt from `MaxKeysPerTag`, and `MaxKeys` still bounds memory.
- The full suite, `-race`, and the go.work compatibility run over all 35 plugins'
  latest tags.

**tenant**, driven end to end with `collagetest`:

- Isolation:
  - acme and globex on one Incremental page and on one Incremental document each
    get their own data;
  - the same holds on a cache hit.
- Lookup order:
  - the static list comes before `Resolve`;
  - a `Bypass` host gets no tenant;
  - an unknown host gets 404.
- Resolver failures:
  - an error gives 503 and is not cached;
  - a panic gives 503.
- Cache bounds:
  - the cache is asked again after `TTL`;
  - 50,000 random hosts stay within `MaxHosts`.
- Each `Init` misconfiguration gives its own error.
- A spoofed `X-Collage-Tenant` header is inert.
- A build gives the warning.

**End to end** (go.work: tenant, sitemap, feed, meta, indexnow):

- Two tenants' sitemaps list different URL sets under different origins.
- canonical and og:url point at the right host.
- An invalidation queues one indexnow batch per origin.

**Each updated plugin:**

- Its own `BaseURL` behaves as before.
- With a resolver, the origin follows the request's host.

## Release order

1. collage v0.42.0: core changes, CHANGELOG, then the docs site (EN and TR).
2. The six updated plugins, each on collage v0.42.0. Release them before anything
   relies on them in the tag CI matrix.
3. collage-tenant v0.1.0: a new repo, added to collage's tag CI matrix.
4. Roadmap core-change log: record the additive changes (`OriginResolver`,
   `Origins`, `BaseURL(rc)`, `VariedContext`, `Entries`) and the observations
   (response-`Vary` leakage of a synthetic dimension; per-host export).

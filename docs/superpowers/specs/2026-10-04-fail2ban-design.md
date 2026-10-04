# elagoht/fail2ban: ban clients that probe or brute-force a site

Date: 2026-10-04
Status: approved design, pending implementation
Roadmap: [v1.0 roadmap](../plans/2026-10-03-v1-roadmap.md), phase 2, the
"fail2ban / probe-guard" item (A3)

## Motivation

The roadmap pairs fail2ban with A3: observing requests the core rejects before
routing. Exploration found two gaps and one non-gap.

- **Early rejections are invisible to `RequestHook`.** `ServeHTTP` answers two
  kinds of request before `plugins.Request` runs (internal/httpx/handler.go):
  - a path with an encoded slash (`%2f`) gets a 404 through `ServeStatus`, which
    reaches `ErrorHook` but not `RequestHook`;
  - a dirty path (`/a/../b`) gets a 301 or 308 to its clean form and reaches no
    plugin at all.

  These are exactly a scanner's fingerprints. RequestHook observers such as otel
  miss them too (accesslog is a middleware and prometheus counts through
  `Metrics.HTTPResponse`; neither changes here).
- **There is no shared client address.** Behind a reverse proxy every request's
  `RemoteAddr` is the proxy. A plugin that bans by `RemoteAddr` bans the proxy,
  and so the whole site. elagoht/ratelimit solves this with its own
  `TrustProxy`/`TrustedProxies`; a second copy in fail2ban would drift from it.
  The roadmap's phase 4 also wants trusted proxies for `X-Forwarded-Proto`.
- **Enforcement needs nothing new.** A plugin middleware answering early is
  enough.

The user wants both scanners (automatic detection) and brute force (failures the
application reports) handled.

## Goals

- **Core (collage v0.46.0), additive:**
  - `ServerConfig.TrustedProxies` and `collage.ClientIP(r)`;
  - `RequestHook` brackets the early rejections too.
- **A plugin, `elagoht/fail2ban` (`github.com/Elagoht/collage-fail2ban`):**
  - counts strikes per client in jails and bans past a threshold;
  - detects scanners on its own, and takes failures the application reports;
  - stdlib only, in memory, bounded.

## Non-goals

- Per-account protection against attempts spread over many addresses.
- A persistent or shared ban store; bans live in one process and end with it.
- 429 and `Retry-After`: that is elagoht/ratelimit's job.
- Moving ratelimit to `ClientIP`: a later, separate change.
- Gating CSRF's `X-Forwarded-Proto` on `TrustedProxies`: a phase 4 decision,
  because it can break sites behind a proxy today.

## Core (collage v0.46.0)

### TrustedProxies

```go
type ServerConfig struct {
	// …
	// TrustedProxies are the addresses and CIDR ranges of the proxies in front
	// of the server ("10.0.0.0/8", "127.0.0.1"). A request from one of them may
	// name its client in X-Forwarded-For; see ClientIP. Empty, the default,
	// trusts no header: the client is always RemoteAddr.
	TrustedProxies []string
}
```

An entry that is neither an address nor a CIDR range makes `collage.New` fail,
naming the entry.

### ClientIP

```go
// ClientIP is the address of the client r comes from.
func ClientIP(r *http.Request) netip.Addr
```

1. Take `RemoteAddr`'s host (with or without a port).
2. If it is not a trusted proxy, it is the client.
3. Otherwise read `X-Forwarded-For` (every value, comma-separated, in order)
   from the right, skipping trusted addresses. The first untrusted address is the
   client. If every address is trusted, the leftmost one is.
4. An entry may carry a port (`9.9.9.9:4567`, `[2001:db8::1]:443`) or brackets
   (`[2001:db8::1]`); these are read as the address. An entry that still does not
   parse (`unknown`, garbage) ends the walk: the last good untrusted address seen
   is the client; if none was seen yet, the client is unknown and `ClientIP`
   returns the zero `Addr` (never the trusted proxy, which would make every
   visitor one client).
5. The header is walked from its end without splitting it whole.
6. A `TrustedProxies` entry with zero bits (`0.0.0.0/0`, `::/0`) is accepted but
   logged at Warn by `collage.New`: it lets any client name any address.
7. Addresses are unmapped (`::ffff:1.2.3.4` → `1.2.3.4`) and lose their zone.
   A `RemoteAddr` that does not parse gives the zero `Addr`.

Only `X-Forwarded-For` is read; `X-Real-IP` and `Forwarded` are not. The trusted
set travels in the request's context, set at the start of `ServeHTTP`, the way
the `Origins` capability does. A request that did not come through collage's
handler gets step 1–2 only.

### RequestHook sees early rejections

`plugins.Request(r)` and its `finish` move before the dirty-path checks in
`ServeHTTP`:

- an encoded-slash path reports `finish(404)`;
- a dirty-path redirect reports `finish(301)` (GET/HEAD) or `finish(308)`.

The development reload channel stays outside everything, as now. The span and
the `HTTPResponse` metric stay where they are. For these requests `RouteOf` is
empty and `r.URL` holds the raw path; a plugin judges it itself. The route
record and origins go on the context before the hook, so a hook reading
`RouteInfo` in `finish` still sees the route of a routed request. The CHANGELOG
lists this as a fix for RequestHook observers that missed these requests.

## The plugin

### Options

```go
const Name = "elagoht/fail2ban"

type Options struct {
	Jails         map[string]Jail `json:"jails"`         // overrides and app jails, by name
	ProbePaths    []string        `json:"probePaths"`    // added to the built-in list
	Allow         []string        `json:"allow"`         // addresses and CIDR ranges never counted or banned
	MaxBanTime    Duration        `json:"maxBanTime"`    // cap for repeat offenders, default "24h"
	MaxTracked    int             `json:"maxTracked"`    // clients with live counters, default 10000
	MaxBans       int             `json:"maxBans"`       // live bans, default 10000
	InDevelopment bool            `json:"inDevelopment"` // default false: nothing happens in development

	OnBan func(b Ban) `json:"-"`
}

type Jail struct {
	MaxRetry int      `json:"maxRetry"` // strikes that ban
	FindTime Duration `json:"findTime"` // window strikes are counted in
	BanTime  Duration `json:"banTime"`  // first ban's length
	Off      bool     `json:"off"`      // the jail counts nothing
}

type Ban struct {
	Prefix netip.Prefix // the client: /32 for IPv4, /64 for IPv6
	Jail   string
	Until  time.Time
	Count  int // bans of this client in the last 24 hours, this one included
}

func New(opts Options) *Plugin
func (p *Plugin) Report(r *http.Request, jail string)
func (p *Plugin) Forgive(r *http.Request, jail string)
func (p *Plugin) Ban(addr netip.Addr, d time.Duration)
func (p *Plugin) Unban(addr netip.Addr)
func (p *Plugin) Bans() []Ban
```

`Duration` is the plugin's own `time.Duration`, read from JSON as "10m" or
nanoseconds, as in cdnpurge, tenant and errortrack.

A jail given in `Jails` takes each zero field from its defaults, so
`{"probe": {"banTime": "6h"}}` changes only the ban length. `off: true` turns a
jail off; a zero `maxRetry` never does, since JSON cannot tell it from an omitted
one.

### Jails

| Jail | Strikes | Default |
|---|---|---|
| `probe` | A path starting (case-insensitively) with a built-in probe prefix — `/.env`, `/.git/`, `/.aws/`, `/wp-login.php`, `/wp-admin`, `/xmlrpc.php`, `/phpmyadmin`, `/cgi-bin/`, `/vendor/phpunit` — or one from `ProbePaths`; and every early rejection (an encoded slash, a dirty path) | 3 in 10m → 1h |
| `notfound` | Every 404 | 50 in 1m → 10m |
| any other name | `Report(r, name)` | 5 in 10m → 15m |

A request that is a probe and a 404 strikes `probe` only.

### Counting

- The client is `collage.ClientIP(r)`, as a prefix: /32 for IPv4, /64 for IPv6.
  A zero address is never counted.
- `OnRequest` classifies the request (so a later middleware rewriting `r.URL`
  cannot change the verdict) and records nothing; `finish(status)` strikes:
  - a probe path → `probe`, unless the request resolved to a page, document or
    action (the site really serves that path; handlers and mounts still count);
  - an early rejection (an encoded slash, a `.`/`..` segment) → `probe`;
  - else a 404 → `notfound`, unless it came from a mount (a missing image on a
    page must not ban its readers).
- Built-in probe prefixes also match as a segment anywhere for `/.env` and
  `/.git/` (`/api/.env`, `/backend/.git/config`), and `/.git` without the slash.
- A browser's subresource request (a `Sec-Fetch-Dest` other than `document`,
  `iframe`, `empty` or absent: `image`, `script`, `style`, …) never strikes:
  `<img src="/.env">` on another page must not ban its readers. A scanner that
  forges the header evades detection; the README says so.
- A request whose `RemoteAddr` is loopback or private, carries
  `X-Forwarded-For`, and whose `ClientIP` is that same `RemoteAddr` (the proxy is
  not in `TrustedProxies`) is not counted, and one Warn per process says
  `TrustedProxies` is probably missing: otherwise the first scanner bans the
  proxy, and with it every visitor.
- A strike is a timestamp in the client's per-jail list, trimmed to `FindTime`.
  `MaxRetry` strikes inside the window ban the client in that jail's name, and
  clear its counters.
- `Forgive(r, jail)` clears that client's counter for the jail. The README
  warns it is only safe when the success proves the failures were the same
  person's (a successful login to the account being guessed); forgiving on any
  success lets an attacker with an account of their own guess without limit.
- `Allow` addresses, banned clients' requests and requests in development
  (without `InDevelopment`) are not counted.

### Banning

- A ban lasts the jail's `BanTime`. A client banned again within 24 hours of its
  previous ban's end gets the jail's `BanTime` × 2^(Count−1), where Count is the
  number of such consecutive bans, capped at `MaxBanTime`. A manual ban keeps
  Count but is not doubled. `Unban` also forgets the client's earlier bans.
- The plugin's middleware answers a banned client with `403`, body `Forbidden\n`,
  `Content-Type: text/plain; charset=utf-8`, `Cache-Control: no-store`. It never
  renders the error page: a flood of banned requests must stay cheap.
- The README tells users to list the plugin first in `Config.Plugins`, so its
  middleware runs before others.
- Each ban is logged at Warn (prefix, jail, until). `OnBan` is called after the
  ban is in place, on the request's goroutine; a panic in it is recovered and
  logged.
- `Ban(addr, d)` bans by hand (jail `"manual"`, no doubling); `d <= 0` does
  nothing; over an existing ban it never shortens it (the later end wins); `Unban` lifts a
  ban and clears the client's counters; `Bans()` returns a snapshot of live bans,
  soonest to end first.

### Bounds

- At most `MaxTracked` clients have counters; adding one past it evicts the
  least recently struck.
- At most `MaxBans` live bans; a new one past it evicts the ban ending soonest.
- Expired bans and empty counters are dropped lazily, on access, and by a sweep
  at most once a minute run from a request (no goroutine).
- The "banned again within 24h" memory is kept with the ban record for 24h after
  it ends and counts against `MaxBans`.

### Errors

| Case | Behaviour |
|---|---|
| An invalid `Allow` entry | Start error (`collage.New`), naming the entry. |
| A negative `MaxRetry`, `FindTime`, `BanTime` or `MaxBanTime`; a negative `MaxTracked` or `MaxBans` | Start error. |
| `Report`/`Forgive` with an unknown jail | A jail with the default settings (5 / 10m / 15m) is used. |
| `Report`, `Forgive`, `Ban`, `Unban` before Init or in development | No-ops, race-free. |
| `OnBan` panics | Recovered, logged; the ban stands. |

## Testing

**Core:**

- `ClientIP` table:
  - no `TrustedProxies`: a forged `X-Forwarded-For` is ignored;
  - behind a trusted proxy: the first untrusted address from the right;
  - every hop trusted: the leftmost;
  - a forged address the client prepends does not win;
  - several `X-Forwarded-For` header lines read in order;
  - an unparsable entry stops the walk;
  - `::ffff:` unmapped; a `RemoteAddr` without a port; a garbage `RemoteAddr`
    gives the zero `Addr`;
  - a request built outside collage's handler uses `RemoteAddr` only.
- An invalid `TrustedProxies` entry fails `collage.New`.
- A `RequestHook` sees `finish(404)` for `%2f` and `finish(301)`/`finish(308)`
  for a dirty path; the reload channel still bypasses it.
- The existing suite and the CI matrix over every plugin's latest tag pass.

**fail2ban**, against a real collage app, with an injectable clock:

- Three `/.env` requests ban; the fourth gets `403` with the plain body and
  `no-store`, and no page is rendered. Another client is unaffected.
- `/a/../b` and `/x%2fy` strike `probe`.
- 50 404s ban; `notfound` with `off: true` never does; `{"probe": {"banTime": "6h"}}` keeps the other defaults.
- `Report(r, "login")` five times bans; `Forgive` resets; an unknown jail uses
  the defaults.
- Strikes older than `FindTime` do not count; a ban ends at `BanTime`; a second
  ban within 24h doubles and stops at `MaxBanTime`.
- With `TrustedProxies`, the `X-Forwarded-For` client is banned, not the proxy;
  without it, a forged `X-Forwarded-For` does not dodge the ban.
- `Allow` is never counted.
- Two addresses in one IPv6 /64 are one client.
- `MaxTracked` and `MaxBans` hold under load; concurrent requests are clean under
  `-race`.
- Development does nothing; `OnBan` is called and its panic recovered; `Ban`,
  `Unban` and `Bans` work.
- Start errors, one per case.

## Release order

1. collage v0.46.0, with the CHANGELOG (`TrustedProxies`, `ClientIP`,
   `RequestHook` seeing early rejections) and the docs.
2. The new public repo `Elagoht/collage-fail2ban` v0.1.0, on collage v0.46.0.
3. Add it to collage's CI matrix, then run a workflow_dispatch.
4. Docs site, EN and TR: `TrustedProxies` in configuration, `ClientIP` in the
   plugin-writing guide, the plugin's entry (39 plugins).
5. Roadmap core-change log:
   - "fail2ban → `TrustedProxies` + `ClientIP`, `RequestHook` sees early
     rejections, additive, v0.46.0";
   - "A3: closed by the above";
   - "ratelimit can move to `ClientIP` (open)";
   - "phase 4's `X-Forwarded-Proto` decision can use `TrustedProxies`".

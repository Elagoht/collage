# Going to production

A collage project is a Go program, so what you deploy is a compiled one:

```
collage build
./bin/mysite
```

`collage build` runs the `go build` somebody would otherwise have to remember —
`CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"` — for this machine by default.
A binary built on a Mac does not run in a Linux container, so for a server name the
target: `collage build -os linux -arch amd64`. `collage build -i` also offers to write a
Dockerfile and a systemd unit into `bin/` beside it. See [the CLI](cli.md).

There is no `collage start`, and there should not be. It could only shell out to
`go run .`, which puts the Go toolchain in your production image and compiles at
every boot, or repeat what `collage build` already does. Running a compiled binary
needs nothing remembered, so nothing needs to remember it for you.

`collage export` is the other thing this project can be — static files, for a site
that needs no server. That is at the end of this page.

## The binary carries the site

A scaffolded project embeds its templates and its static files, so the binary runs
from any working directory. Nothing has to be copied next to it, and a container
image can be the binary and nothing else:

`collage build -i` writes this into `bin/`, beside the binary — so the command is
`docker build -f bin/Dockerfile .`, which the build output prints. It is reproduced
here, for a project named `mysite` built with Go 1.26, so you can read it before you
run it; the file it writes names your project and the Go version that built it.

```dockerfile
# Built by "collage build -i".
#
# Two stages, and the second one holds the binary and nothing else: a collage
# project embeds its templates and its static files, so there is nothing beside
# the binary to copy. CGO is off, which is what makes the binary static enough
# for a distroless base.
FROM golang:1.26 AS build
WORKDIR /src
# Dependencies first, so editing your own code does not re-download them. If
# your go.mod has a replace directive pointing at a path in this repository,
# move "COPY . ." above this line — the replaced module is not here yet, and
# go mod download will say so.
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /mysite .

FROM gcr.io/distroless/static-debian12
# WORKDIR matters: the rendered-page cache is a path, so it lands wherever the
# process was started. Everything else about this project travels in the binary.
WORKDIR /srv
COPY --from=build /mysite /usr/local/bin/mysite
ENV HOST=0.0.0.0 PORT=8080
# Set this to at least 32 random bytes, kept with your other secrets. Without
# it a key is generated per process, and every form submitted before a restart
# is refused after it.
# ENV COLLAGE_CSRF_KEY=
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/mysite"]
```

`CGO_ENABLED=0` because collage and the standard library need no C, and a static
binary is what makes the second stage able to be `distroless/static`. The
`COLLAGE_CSRF_KEY` line is left for you; supplying it from your platform's secret
store rather than writing it into the image keeps it out of every copy of the
image.

Two things that are only true because the project embeds its files: the image needs
no `COPY` of templates or assets, and nothing breaks if the container's working
directory is not the project's.

## What to set

| Variable | Why |
| --- | --- |
| `HOST` | `0.0.0.0` in a container; the default `localhost` accepts nothing from outside it |
| `PORT` | whatever your platform gives you |
| `COLLAGE_CSRF_KEY` | **set this.** Without it a key is generated per process: every form submitted before a restart is refused after it, and one instance refuses what another issued |

```
COLLAGE_CSRF_KEY=$(head -c 32 /dev/urandom | base64)
```

Keep it with your other secrets, and keep it the same across every instance and
across restarts. Changing it invalidates outstanding forms, and every cached page
carrying a form renders again on its next request — pages without one are
unaffected. That is correct, and worth knowing before you rotate it during
traffic.

## Graceful shutdown and draining

`ListenAndServe` traps `SIGINT` and `SIGTERM` and shuts down gracefully on either.
`App.Shutdown(ctx)` does the same when you call it yourself. The order is fixed:

1. **Drain.** The ctx `ListenAndServe` gave `ServeHook` plugins in `OnServe` is
   cancelled, so a scheduler starts no new work. Every plugin implementing
   `DrainHook` hears `OnDrain()`, once — a health plugin turns its readiness
   check false here. Keep-alives are turned off:
   idle kept-alive connections close at once, busy ones after their current
   response, and their clients reconnect through the balancer. The port stays open and requests are served as
   normal, for `Server.DrainDelay`.
2. **Streams.** Development reload streams and plugin streams are closed; they
   never end on their own.
3. **Server.** The port closes and requests in flight get up to
   `Server.ShutdownTimeout` (10s by default) to finish.
4. **Plugins.** Every plugin's `Shutdown` runs, which is where a plugin flushes
   whatever it was holding.

`DrainDelay` is 0 by default: no wait, and a single instance behind nginx, Caddy
or Cloudflare stops as fast as it always has. Set it when a load balancer has to
notice the instance is leaving before the port closes — a few seconds longer than
its readiness check takes to fail:

```go
app, err := collage.New(&collage.Config{
	Server: collage.ServerConfig{
		DrainDelay:      10 * time.Second,
		ShutdownTimeout: 10 * time.Second,
	},
})
```

The two add up. On a signal, `ShutdownTimeout` starts when the drain ends, so a
stop can take `DrainDelay + ShutdownTimeout`. When you call `App.Shutdown(ctx)`
yourself, one ctx covers both the drain and the wait for requests in flight, and
`ShutdownTimeout` is not used: give it a deadline of at least `DrainDelay` plus
the time your requests need, or the drain uses up the time they would have had.
A plugin's `Shutdown` may run a little past that deadline:
[`elagoht/jobs`](https://github.com/Elagoht/collage-jobs) waits up to one more
second for a job that ignores its ctx, so with it a stop can take
`DrainDelay + ShutdownTimeout + 1s`. Whatever stops the process has to wait
longer than that sum before it kills it:

- **systemd:** `TimeoutStopSec` greater than the sum. The unit
  `collage build -i` writes uses 30s, which covers the defaults; raise it with
  `DrainDelay`.
- **Kubernetes:** `terminationGracePeriodSeconds` greater than the sum. Point the
  probes at the [`elagoht/health`](https://github.com/Elagoht/collage-health)
  plugin's endpoints, so readiness fails as soon as the drain starts (see
  [Health checks](#health-checks) if your project has a `/healthz` document):

```yaml
spec:
  terminationGracePeriodSeconds: 30   # > DrainDelay + ShutdownTimeout (+ 1s with elagoht/jobs)
  containers:
    - name: app
      readinessProbe:
        httpGet: { path: /readyz, port: 8080 }
        periodSeconds: 2
      livenessProbe:
        httpGet: { path: /healthz, port: 8080 }
```

Pressing Ctrl-C twice — a second `SIGINT` or `SIGTERM` — ends the drain's wait at
once and goes straight on to stopping the server. A `Shutdown(ctx)` whose ctx is
done ends it too. In development mode `DrainDelay` is ignored, so restarts stay
instant; `OnDrain` still fires. A `Shutdown` before anything serves tells the
plugins and does not wait, since there is no traffic to drain.

`OnServe` is called only by `ListenAndServe`. An application that runs its own
`http.Server` over `app.Handler()` gets no `OnServe`, so it must start such
plugins itself and cancel their ctx when it begins to stop — for
[`elagoht/jobs`](https://github.com/Elagoht/collage-jobs), call its
`Start(ctx)` once the server is listening. It must also call `app.Shutdown(ctx)`
after its server has stopped: that is what runs the plugins' `Shutdown`, and
without it a queue's waiting items vanish without even being logged.

```go
// Init first, and its error: app.Handler() would only log it and answer 503,
// and the jobs plugin's Start panics before its Init has run.
if err := app.Start(); err != nil {
	return err
}
ln, err := net.Listen("tcp", ":8080")
if err != nil {
	return err
}
srv := &http.Server{Handler: app.Handler()}
go srv.Serve(ln)
ctx, stopJobs := context.WithCancel(context.Background())
j.Start(ctx) // the port is bound: start the jobs

// On shutdown:
stopJobs()                // the drain: no more triggers; Enqueue still accepts
srv.Shutdown(shutdownCtx) // requests in flight finish
app.Shutdown(shutdownCtx) // the plugins stop; jobs finishes its queues and refuses more
```

## TLS, and what serves it

collage serves plain HTTP. There is no `ListenAndServeTLS`, deliberately:
terminating TLS well means certificates, renewal, ALPN, HSTS and a redirect from
`:80`, and every platform this runs behind — a load balancer, a reverse proxy,
Cloudflare, Fly, Render — already does it better than a framework flag would.

Put it behind something, or serve it yourself: `app.Handler()` is an ordinary
`http.Handler`, so an `http.Server` of your own can wrap it with whatever you need.

```go
srv := &http.Server{Addr: ":443", Handler: app.Handler()}
srv.ListenAndServeTLS(certFile, keyFile)
```

Doing that means you own the timeouts and the shutdown that `ListenAndServe` was
handling for you.

## Timeouts

`ListenAndServe` sets `Server.ReadTimeout` (15s), `ReadHeaderTimeout`,
`WriteTimeout` (30s) and `IdleTimeout` (60s) on its server. `ReadHeaderTimeout` is
what drops a client that sends its headers a byte at a time; unset, it is
`ReadTimeout`, as it always was. Set it shorter to free such connections early while
a large upload still gets the whole `ReadTimeout` for its body:

```go
Server: collage.ServerConfig{
    ReadTimeout:       60 * time.Second, // uploads
    ReadHeaderTimeout: 5 * time.Second,
},
```

A negative value in any of them is `collage.ErrNegativeDuration`. `collage dev`'s
proxy has its own fixed 10s header timeout, which is development only.

## Behind a proxy: `TrustedProxies`

Behind a proxy, every request's `RemoteAddr` is the proxy's. The proxy names the
real client in `X-Forwarded-For`, but so can anyone else: a client sending its own
`X-Forwarded-For` is not to be believed. `Server.TrustedProxies` says whose to
believe:

```go
Server: collage.ServerConfig{
    TrustedProxies: []string{"10.0.0.0/8", "127.0.0.1"},
},
```

Each entry is an address or a CIDR range; one that is neither makes `collage.New`
fail. `collage.ClientIP(r)` then answers who the client is: `RemoteAddr`'s host,
unless that is a trusted proxy — then the header is read from the right, skipping
trusted addresses, and the first untrusted one is the client (the leftmost, when
every one is trusted). Empty, the default, trusts no header, and `ClientIP` is
always `RemoteAddr`.

A header entry may carry a port (`9.9.9.9:4567`, `[2001:db8::1]:443`) or brackets
(`[2001:db8::1]`), as some proxies write it; it is read as the address. An entry
that is still not an address — `unknown`, which some proxies send — ends the walk.
If no untrusted address was found before it, the client is unknown and `ClientIP`
is the zero `netip.Addr`: never the proxy, which would make every visitor the same
client. A plugin keying on the client skips such a request.

List every hop between the client and the server: your own proxies and, behind a
CDN, the CDN's published ranges too. A hop left out is taken for the client, and
every visitor coming through it becomes one. List only proxies you run, or your
platform's documented ranges: trusting a range a client can send from lets that
client name any address it likes. An entry with zero bits (`0.0.0.0/0`, `::/0`)
trusts everyone; `collage.New` accepts it but logs a Warn saying so.

The same list decides whose `X-Forwarded-Proto` is believed. The forgery cookie is
`Secure` when the request arrived over TLS, and behind a proxy that terminates TLS
the proxy says so in that header. With `TrustedProxies` set, a request whose
`RemoteAddr` is not a listed proxy cannot set it: a client reaching the port
directly over plain http gets a cookie it can send back over plain http. Empty, the
header is believed from anyone — the behaviour before the list existed, and right
only when nothing but your proxy can reach the server.

## Development mode stays on this machine

`DevMode` serves things production must not: error pages with stacks and source,
the reload stream, a development toolbar. Two guards keep them here:

- **A foreign `Host` is refused.** In development the server answers `403` to a
  request whose `Host` is not localhost (or a name under `.localhost`), an IP
  address, a reserved name — `example.com`, `example.net`, `example.org` and names
  under them, or a name under `.example`, `.test` or `.invalid` — `Server.Host`, or
  a name in `COLLAGE_DEV_HOST`. A page on another site can make its own name
  resolve to `127.0.0.1` — DNS rebinding — and would then be same-origin with the
  development server; its `Host` is still its own name. Reserved names are allowed
  because nobody can register one to point at you; they are what tests use
  (httptest's requests are for `example.com`) and what `/etc/hosts` entries
  conventionally use.
- **A reachable address is warned about.** When `ListenAndServe` binds anything but
  loopback in development — `0.0.0.0`, `::`, a LAN address — it logs a Warn saying
  the development pages expose the application's internals. `collage dev` logs the
  same when its own proxy is bound that way.

### Reaching development by another name: `COLLAGE_DEV_HOST`

`COLLAGE_DEV_HOST` is a comma-separated list of extra names a development server
answers. Set it for:

- a docker-compose service name, `http://app:6060` from another container:
  `COLLAGE_DEV_HOST=app`;
- a name in `/etc/hosts` outside the reserved ones;
- several tenant hosts in one development server (or use `*.localhost` names, which
  need no setting);
- reaching a server bound to `0.0.0.0` from a phone by the machine's LAN name:
  `HOST=0.0.0.0 COLLAGE_DEV_HOST=mybox.lan`. By IP address it needs nothing.

```
COLLAGE_DEV_HOST=app,mybox.lan
```

An application in development mode reads it from its own process environment — run
directly, set it in the shell or wherever the process gets its environment.
`collage dev` reads it from the shell or `.env.development`, applies it to its
proxy, and hands the program the list plus its own `HOST`, since the program
listens on loopback but sees the browser's `Host`.
A refused request's 403 names the setting. A tunnel such as ngrok is refused
unless you list its name — on purpose: it puts the development pages on the
internet.

Neither applies in production, where `DevMode` is off and the `Host` is whatever
your DNS sends.

## Caching

A scaffolded project caches rendered pages to disk under `.cache`, which survives a
restart so a redeploy does not re-render the whole site into a cold cache.

The cache is namespaced by a hash of the running binary, so a new build never serves
the previous one's pages and you never have to remember to clear anything. In a
container the directory is inside the container, so every instance fills its own;
that is fine — the cost is one render per page per instance.

**`.cache` is relative to the working directory, and that is the one thing about a
scaffolded project that is not.** Templates and static files are embedded, so the
binary runs from anywhere; the cache directory is a path, so it lands wherever you
started the process. Run the binary from `/tmp` and the cache is `/tmp/.cache`. In a
container, set `WORKDIR` or give `Cache.Dir` an absolute path, and make sure it is
writable:

```dockerfile
WORKDIR /srv
```

A cache it cannot write to is not an error — the write fails, the page is served,
and the next request renders it again. Quiet and slow rather than broken, which is
the right failure for a cache and the wrong one to leave in place unnoticed.

Pages declare how they are cached, and the declaration is the whole mechanism:

```go
Static()               // render once, serve until invalidated
Incremental(time.Minute) // re-render at most this often
Dynamic()              // never cached
```

A page that declares none is static when nothing it renders has a data handler,
and dynamic when something does.

Invalidate by tag when content changes, which is what a webhook from a CMS is for:

```go
app.InvalidateTags(ctx, "article:"+slug)
```

See [caching](caching.md).

### A memory cache and the container's limit

A `"memory"` cache is bounded by `MaxBytes` — 256 MiB of stored pages unless you
set it — and the bound holds: the live heap stops growing once the cache is full.
The process does not stop there. Go's collector lets the heap grow to about twice
what was live after the last collection before it runs again, so a full 256 MiB
cache can mean a process well past 512 MiB. Measured on a page of about 14 KB
asked for under 30,000 distinct URLs, the process settled at about 720 MB.

Tell the runtime what it has with `GOMEMLIMIT`, a little under the container's
limit, and it collects harder as it approaches it instead of being killed:

```dockerfile
# In a 512 MiB container.
ENV GOMEMLIMIT=400MiB
```

The same run with `GOMEMLIMIT=320MiB` stayed at about 430 MB. The limit is soft:
it is what the runtime aims for, not a cap it enforces, so leave `MaxBytes` well
below it — what the cache holds is live and cannot be collected however hard the
runtime tries. A cache sized at a third to a half of the limit leaves room for the
renders themselves — in a 512 MiB container, lower `MaxBytes` from its default to
128–192 MiB. A `"disk"` cache, which is what a scaffolded project uses,
keeps its pages on disk and does not need this.

## Health checks

Liveness and readiness come from the
[`elagoht/health`](https://github.com/Elagoht/collage-health) plugin: `/healthz`
answers `200` while the process serves requests, and `/readyz` answers `200`
until a check of yours fails or a drain starts, when it turns `503`. Point every
platform at those two paths — the Kubernetes probes in
[Graceful shutdown and draining](#graceful-shutdown-and-draining), or a load
balancer's health check at `/readyz` in front of a systemd unit — so readiness
fails as soon as the drain starts. List the plugin before any plugin that can
refuse or answer a request; its README says why and in which order.

A project scaffolded with the demos (`collage new --template demo`) ships a
`/healthz` document, `documents/health.go`. Once `elagoht/health` is added, the
plugin's middleware answers `/healthz` first and the document is never reached;
startup does not catch this, because the plugin checks pages, not documents.
Delete the document, or move the plugin's liveness endpoint with `livePath`.

A project without the plugin can still answer its probes with documents of its
own — bytes and a content type, no templates, so a check cannot start failing
because a template did. The demo's `documents/health.go` is one to copy for
liveness. Such a readiness document does not know about the drain, though, so it
keeps answering `200` until the port closes.

## A static site instead

If nothing on the site needs a server, `collage export` renders it to files you can
put on any static host:

```
collage export -clean
```

The output is in `dist/`, with the site's own `404.html` beside it — one per
locale, `404.html` and `tr/404.html`, because hosts differ on which they consult.
It comes from whatever was registered with `RegisterNotFoundPage`, and its render
strategy is not consulted: whether a page is worth caching and whether it belongs in
an export are different questions, and a not-found page is almost always `Dynamic()`
because it is never worth caching. A site exported without one answers an unknown
URL with whatever the host decided to show — somebody else's page, in somebody
else's language, with none of the navigation a reader needs to get back.

Dynamic pages — declared `Dynamic()`, or declaring nothing and rendering a data
handler — are skipped and named, and so is a page carrying a form,
with the reason — a form needs somewhere to post to, and a static host is not it.
The one exception is the not-found page, which a static host needs as a file: one
carrying a form fails the export. See [actions](actions.md).

Look at it before you deploy it:

```
collage serve
```

which serves `dist/` the way a static host does — clean URLs, no directory
listings, `404.html` with a 404, nothing cached. Opening `dist/index.html` from the
file system does not work, because a `file://` page has no root and every absolute
link in the export is broken.

## Static hosts

A server sends headers with every page and answers redirects; a static host sends
what its own configuration files tell it to — Netlify's `_headers` and
`_redirects`, Vercel's `vercel.json`, and so on. An export carries what the server
would have said, so that a plugin can write those files.

**Headers are captured.** Once every file is written, and when a plugin
implementing `BuildFinishedHook` is registered to read them, the build asks the
application's own handler for each file's path — in-process, no network — twice,
and records the status and the headers it answered with on the file
(`BuiltFile.Status`, `BuiltFile.Headers`); with no such plugin it asks nothing.
The requests carry the host of `Config.BaseURL`, and come over HTTPS when its
scheme is `https`, so a header sent only over HTTPS — collage-secure's
`Strict-Transport-Security` — is captured too. `BuiltFile.Captured` marks every
file the build asked for, also one whose capture failed (its `Status` is then 0),
so a plugin can tell it from a file the build made itself. Whatever your middleware and plugins
set — `Cache-Control`, `Content-Security-Policy`, `X-Frame-Options` — comes along
without being declared again. Left out:

- headers about one response rather than the file: `Date`, `ETag`,
  `Last-Modified`, `Content-Length`, `Set-Cookie`, `Vary`, `Content-Encoding`,
  `Transfer-Encoding`, `Connection`, `Age`, and development's
  `X-Collage-Render-Time`;
- a header whose value differs between the two answers, such as a CSP nonce. A
  file cannot carry a new one per reader, so it is left out and an
  `unstable-header` warning names it;
- the `404.html` pages and the root redirect, which the build makes itself.

A path answered with something other than one 2xx is a `capture-status` warning,
one not answered at all a `capture-failed` warning, and a build in development
mode a `capture-dev-mode` warning — its `Cache-Control: no-store` is not what you
want deployed.

A page with a CSP nonce, or anything else a `PersonaliseHook` marks `Personal`,
is answered `private, no-store` to a reader, but not to the capture: the hook
still runs, the nonce header differs between the two answers and is left out,
and the exported file gets the `Cache-Control` the page's strategy gives — for an
`Incremental(time.Hour)` page, `public, max-age=3600` — so a host can cache it.
Pages still answered with `Cache-Control` `private` or `no-store` beside a header
that differs between the answers — the `Cache-Control` set some other way, such
as by a middleware wrapping the response — are one `capture-personal` warning:
the header that differs is left out, so the exported file is no longer personal,
and the `Cache-Control` only keeps a host from caching it. A development build
does not raise it, since `capture-dev-mode` already covers its `no-store`. None
of these warnings fails the build.

Middleware sees these requests. A plugin that counts or limits traffic —
analytics, a rate limiter, a ban list — should let a request through untouched
when `collage.IsCapture(r.Context())` is true: it is the build, not a reader.
Middleware that sets headers should not skip it, since what it sets is what gets
deployed.

**Redirects reach the build hook.** `BuildFinishedEvent.Redirects` holds every
redirect the site declares — pages' (`WithRedirect`, `WithPermanentRedirect`),
documents', and every plugin implementing `collage.RedirectSource` — each with its
status and where it came from. A page registered only as the not-found or
error page is never matched, and its redirects are not among them. Two
redirects the router would take for one — `/old` and `/old/`, `/blog/{slug}` and
`/blog/{id}` — fail the build with `collage.ErrDuplicateRedirect`, and a redirect
matching the path of a file the build wrote (`/about` or `/about/` beside
`about/index.html`, or `/docs/{rest...}` beside `docs/intro/index.html`) with
`collage.ErrRedirectShadowsFile`: on a host, which one wins would be the host's
call, not yours. A placeholder `From` like `/{slug}` shadows every file the
build wrote at that depth — `about/index.html`, `feed.xml`, `404.html` included —
so it fails the build too. A plugin's rules
are checked as a registered redirect is — a
`From` the router can parse, every placeholder in `To` captured by it — except
that `To` may be an `http` or `https` URL, and a 410 has no `To`. Braces in a
`To` are always a placeholder, also in an absolute URL's query
(`https://example.com/?q={slug}`).

**`elagoht/deploy` writes the host's files.** Collage itself writes none: the
plugin takes the captured headers and the redirects from the build hook and
writes them in the form the host you name reads, warning about whatever that host
cannot carry:

```json
{ "elagoht/deploy": { "target": "netlify" } }
```

`target` is one of `netlify`, `cloudflare`, `vercel` or `github-pages`; without
one the plugin writes nothing, removes what the last export wrote for a target,
and says so. It records what it wrote in
`.collage-deploy.json` and replaces those files on the next export into the same
directory, so `collage export` without `-clean` works with it. A header every
captured file shares goes in a `/*` rule, and `/*` also reaches the files other
plugins write into the output — share cards, a search index — so `Content-Type`,
`Content-Disposition` and `Content-Language` are never put there, only at each
file's own path; a `Content-Type` the file's extension implies (`text/html;
charset=utf-8` for a page) is not written at all, and is left to the host.

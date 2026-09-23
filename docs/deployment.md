# Going to production

A collage project is a Go program, so what you deploy is a compiled one:

```
collage build
./bin/mysite
```

`collage build` runs the `go build` somebody would otherwise have to remember —
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w"` — and
defaults to linux/amd64 rather than this machine, because a binary built on a Mac
does not run in a Linux container. `collage build -i` also offers to write a
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
here so you can read it before you run it.

```dockerfile
FROM golang:1.26 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /mysite .

FROM gcr.io/distroless/static-debian12
COPY --from=build /mysite /mysite
WORKDIR /srv
ENV HOST=0.0.0.0 PORT=8080
EXPOSE 8080
ENTRYPOINT ["/mysite"]
```

`CGO_ENABLED=0` because collage and the standard library need no C, and a static
binary is what makes the second stage able to be `distroless/static`.

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
across restarts. Changing it invalidates outstanding forms and the page cache, which
is correct and worth knowing before you rotate it during traffic.

## Signals and shutdown

`ListenAndServe` traps `SIGINT` and `SIGTERM` and shuts down gracefully on either:
it stops accepting connections, waits up to `Server.ShutdownTimeout` (10s by
default) for requests in flight, and then returns. A container that sends `SIGTERM`
and waits gets a clean drain without anything added.

Every plugin's `Shutdown` runs as part of that, which is where a plugin flushes
whatever it was holding.

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

Two pages declare how they are cached, and the declaration is the whole mechanism:

```go
Static()               // render once, serve until invalidated
Incremental(time.Minute) // re-render at most this often
Dynamic()              // never cached
```

Invalidate by tag when content changes, which is what a webhook from a CMS is for:

```go
app.InvalidateTags(ctx, "article:"+slug)
```

See [caching](caching.md).

## Health checks

A scaffolded project answers `/healthz` with `{"status":"ok"}`. It is a document
rather than a page — bytes and a content type, no templates — so a health check
cannot start failing because a template did.

Point your platform's liveness check at it. It reports that the process is up and
serving, which is what a liveness check is for; a readiness check that also wants to
know whether your database is reachable is a document of your own, written the same
way.

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

Pages declared `Dynamic()` are skipped and named, and a
page carrying a form is refused outright — a form needs somewhere to post to, and a
static host is not it. See [actions](actions.md).

Look at it before you deploy it:

```
collage serve
```

which serves `dist/` the way a static host does — clean URLs, no directory
listings, `404.html` with a 404, nothing cached. Opening `dist/index.html` from the
file system does not work, because a `file://` page has no root and every absolute
link in the export is broken.

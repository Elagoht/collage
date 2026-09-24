# Changelog

## v0.5.1

- **Licensed under MIT.**
- **`collage dev` rebuilds and restarts on a Go change**, with no tool to install.
  It watches what the program is made of — `.go` files, `go.mod`, `go.sum`, the
  environment file — and never the cache, `dist`, `bin` or hidden directories, so
  nothing the running program writes sets it off. The new build is made before the
  old process is stopped: a change that does not compile leaves the last good
  build serving. See [docs/cli.md](docs/cli.md#rebuilding-on-change).
- A cancelled child process is interrupted and given ten seconds to drain before
  it is killed, rather than killed outright.
- CI runs `staticcheck` and `govulncheck`.
- The repository has a `SECURITY.md` with a private reporting channel, a
  `CONTRIBUTING.md`, and a stability statement in the README. The internal
  planning documents are gone.

## v0.5.0

### Breaking

**The URL is the only thing that selects a locale.** `Accept-Language` and the
locale cookie no longer choose one, and `LocaleConfig.DisableHeaderLocale`,
`DisableCookieLocale` and `CookieName` are gone. They produced one URL with
different content per reader, and a Turkish browser following a link to `/about`
got a 404: the header moved the lookup into the `tr` tree, where that path did not
exist. A URL with no locale prefix is in `Default`; `/tr/...` is in `tr`.
Negotiate in middleware — redirect to `/tr/`, or render one URL per language and
declare it with `collage.Vary`. See [docs/routing.md](docs/routing.md#locales).

A static build reaches a non-default locale through its prefix, the URL that
reaches it over HTTP, instead of through a synthetic header.

### Added

- **`app.Use`** takes standard `func(http.Handler) http.Handler` middleware. It
  runs before routing, inside the span, metrics and panic guard, and what it puts
  in the request context is what data handlers read.
- **`app.Handle(prefix, handler)`** mounts any `http.Handler` — an API router, a
  gRPC gateway. Collage applies no forgery check, body limit or cache to it; an
  overlap with a route or a mount is refused at startup.
- **`collage.Vary(r, header, value)`** declares, from middleware, that a response
  depends on a header. The resolved value enters the cache key, in a section a
  query string cannot reach, and the header name goes into `Vary`.

See [docs/http.md](docs/http.md).

## v0.4.6

- **`collage dev` loads `.env.development`, or `.env`** when there is none — one
  file, never both. A variable set in the shell wins, `COLLAGE_DEV=1` is always
  set, and a malformed line stops the command naming the file and line. Only
  `dev` reads these files; `build`, `export` and the built binary never do.
- **A new scaffold.** `collage new` now writes a home page and a page of live
  demos — an API action that invalidates a cached page by tag, a plain HTML form,
  a fragment with its own URL, a JSON document — split into `pages/`,
  `fragments/`, `actions/`, `documents/` and `store/`, with tests for each. It
  ships a `.env.example`, and its `.gitignore` ignores `.env` and
  `.env.development`.
- The skip reason for a page with a form no longer tells you to make it
  `Dynamic()`. A page with a form can be `Static()` on purpose; it is simply
  served rather than exported.
- The example applications left the repository.

## v0.4.5

- A fragment or page an action answers with carries the reader's forgery token.
  It used to ship the cache marker in its place, so a form that replaced itself —
  and every fragment reached through `WithFragmentPath` — was refused with a 403 on
  its next submission.
- The forgery check reads `multipart/form-data`. That is what
  `fetch(url, {method: "POST", body: new FormData(form)})` sends, and it was refused
  with a valid `_csrf` field in it.
- `collage export` skips a `Static()` page that carries `{{csrfToken}}` and says
  why, instead of failing the whole export. It still writes no file: a built site
  has no server to take the form. (The not-found page is still a failure — a
  static host needs it as a file.)
- `collage.DataHandler` adapts a handler that returns a concrete type to
  `DataHandlerFunc`, so an application needs no `any` and no adapter of its own
  per view type. The scaffold uses it.
- In development a generated `Security.CSRFKey` is logged at info rather than as
  a warning, and the scaffold's README runs `collage dev` first and says why plain
  `go run .` warns.

## v0.4.4

- A scaffolded project reads `plugins-config.json`. It was written into every new
  project and never loaded, so a plugin's settings did nothing — a configuration
  file that is written and never read is the quietest kind of broken.
- Its layout has `{{hoist "head"}}`, so a plugin that contributes to the document
  head has somewhere to land, and its pages hoist their own `<title>` through it.
- `collage version` reports the version the binary was built from, rather than the
  constant that had said 0.1.0 since 0.1.0.

## v0.4.1

- `collage new` scaffolds a not-found page, so a new project's export has a
  `404.html` rather than leaving an unknown URL to whatever the host shows.
- A build no longer reports pathless pages as skipped. Every error page is one —
  they are reached by failing rather than by matching — and naming each of them
  every build said nothing anyone could act on, while the not-found page among them
  *was* in the output, as `404.html`.

## v0.4.0

### Breaking

**`collage build` now compiles the project; the static export moved to `collage
export`.** In every other framework `build` means "compile my application for
production", so somebody typing it here got something else entirely — and nothing
produced what they actually needed to deploy. `collage export` does what `collage
build` used to do, with the same flags.

**A page answers `GET` and `HEAD`; any other method is a 405.** It used to render
the page for every method, which is the wrong answer to a form submission and wrong
in the quietest possible way: the reader got a page that looked like nothing
happened while the application never saw the submission. Declare an `Action` for the
methods a URL should answer.

**A render that produces no markup is a 500 rather than an empty 200.** The static
builder has always refused to write one; serving one was the same mistake with a
status code on it.

### Added

- **Actions.** `WithAction` gives a page's own URL a POST — what an HTML form needs,
  since a form's action is the page it is on — and `RegisterAction` gives one a URL
  of its own, for a webhook or a JSON endpoint. `ActionResult` answers with a
  redirect, one fragment, a whole page, or bytes. See [docs/actions.md](docs/actions.md).
- **Request-forgery protection.** `{{csrfToken}}` renders the hidden input; a signed
  double-submit cookie verifies it, needing a key and no session store. A page with
  a form is still cached: the body carries a marker and each reader's own token is
  substituted on the way out. Set `Security.CSRFKey` in production.
- **Fragments at their own URLs.** `WithFragmentPath` opens one fragment at a URL,
  so part of a page can be refreshed without a client framework.
- **Sibling fragments fetch concurrently.** A page of independent parts makes its
  upstream calls at once rather than one after another. `collage.Once` collapses the
  same fetch made by two of them.
- **Content-addressed asset URLs.** `{{asset "/static/app.css"}}` renders
  `/static/app.<hash>.css`, served `immutable`.
- **Request coalescing.** One render serves every request waiting for the same cache
  key, so an expiring popular page costs one render rather than one per request.
- **`collage serve`** serves a static export the way a static host does, and
  **`collage export` now writes the site's own `404.html`**, one per locale.
- **`collage build -i`** offers a Dockerfile and a systemd unit beside the binary.
- **`PrintBuildReport`** summarises a build so its skips cannot be missed.
- **A terminal-shaped default log** when no `Logger` is configured and the output is
  a terminal. Anywhere else it is `slog.Default()`, unchanged.
- **CI**, running the framework's suite with `-race`, the examples, and the
  published plugins at a tag.

### Fixed

- Editing a template or a stylesheet is visible without a restart, even when they
  are embedded: development prefers the directory on disk, and a mount stops
  remembering a file's content hash.
- A cached page is never served in development, which was hiding the reload above.
- A refusal the request earned — a 405, a missing forgery token — is logged below
  error level. A bot probing forms should not bury the failures that are real.
- A static build refuses to write a page whose forgery token was never resolved,
  rather than shipping a form that cannot work.
- Hoisting no longer depends on which goroutine finished first.

### Scaffold

`collage new` produces two pages — one `Static()` that builds, one `Dynamic()` with
a form that cannot — a data handler, six tests, embedded templates and assets, a
disk cache, `/healthz`, and a deployment section that does not say "run go build".

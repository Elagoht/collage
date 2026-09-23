# Changelog

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

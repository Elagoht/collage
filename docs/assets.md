# Assets

An **asset mount** serves an `fs.FS` under a URL prefix, with file semantics:
`Range` requests, `If-Range`, `206 Partial Content`, `Last-Modified`, `HEAD`, and
a content-hash `ETag`.

```go
root, err := os.OpenRoot("./static")
if err != nil {
	log.Fatal(err)
}
if err := app.Mount("/static/", root.FS()); err != nil {
	log.Fatal(err)
}
```

That is the whole API. `fs.FS` covers both of the sources that matter — a
directory on disk and an `embed.FS` — through one interface.

> ## `os.DirFS` is not a security boundary
>
> Go's own documentation states that `os.DirFS` does not prevent symlink
> traversal: a symlink placed inside the mounted directory escapes it, and the
> file it points at is served. That is not a bug in this framework or in Go — it
> is `os.DirFS`'s documented contract, and it makes `os.DirFS` the wrong tool for
> serving a directory to the internet.
>
> Use `os.OpenRoot`, which the kernel enforces:
>
> ```go
> root, err := os.OpenRoot("./static")
> if err != nil {
>     log.Fatal(err)
> }
> app.Mount("/static/", root.FS())
> ```
>
> Every path an `os.Root` opens is resolved relative to the directory it holds,
> and a symlink leaving that directory fails to open at all. Go 1.24 added it;
> this module targets Go 1.26, so there is no version of this framework where
> `os.DirFS` was the only option.
>
> The framework cannot close this for you. It receives an `fs.FS` and calls
> `Open` on it; containment is a property of the `fs.FS` you hand it, and the
> framework has no way to inspect one. What it *does* do is reject `..`, absolute
> paths and empty path elements before `Open` is ever called — see
> [Path resolution](#path-resolution) — which stops a traversal expressed in the
> URL. It cannot stop one expressed in the filesystem.
>
> Keep the `*os.Root` open for the life of the process. Its `fs.FS` serves every
> request, so closing it breaks the mount.

An `embed.FS` has no symlinks at all and is safe by construction. It is also the
right choice when a binary should run from any working directory:

```go
//go:embed static
var assetsFS embed.FS

// mountAssets serves the embedded "static" directory at "/static/".
func mountAssets(app *collage.App) error {
	static, err := fs.Sub(assetsFS, "static")
	if err != nil {
		return err
	}
	return app.Mount("/static/", static)
}
```

`fs.Sub` matters: `embed.FS` names every file by its path in the source tree, so
without it the stylesheet answers at `/static/static/app.css`. A mount serves
what it is given and does not guess at a root inside it.

## Content-addressed URLs

Link a mounted file with `asset`, not by hand:

```html
<link rel="stylesheet" href="{{asset "/static/app.css"}}">
```

What goes into the page is the file's own name with its content hash in it:

```html
<link rel="stylesheet" href="/static/app.0d5f2b53aebf6c72.css">
```

and that URL is served with

```
Cache-Control: public, max-age=31536000, immutable
```

The hash is why that is honest rather than optimistic. A name derived from the bytes
cannot describe anything else, so there is nothing for a client to revalidate: when
the file changes the name changes, the old URL is never requested again, and the new
one arrives on the next page load. A plain name cannot be served that way at any
lifetime — sooner or later it hands someone a stylesheet that no longer matches the
page it is styling. That is why the mount's own `Cache-Control` stays short, and why
`WithCacheControl` is about the plain names rather than these.

Three details worth knowing.

**A file that does not exist is an error, not a URL.** `{{asset "/static/typo.css"}}`
fails the fragment. The alternative — emitting the path unchanged — is a page that
renders successfully while linking a stylesheet that 404s: broken, and reporting
itself as fine.

**The hash in a request is verified.** A URL carrying a hash that is not the file's
is a 404, not a hit. Serving it would pin a mistake in front of every client behind
a shared cache, for a year.

**Plain names still work.** `/static/app.css` is served exactly as before, with the
mount's own `Cache-Control`. Nothing existing breaks; it simply cannot be cached
hard.

## In development, nothing is remembered

A content-addressed name and an editable file are in direct conflict. The hash is
normally computed once and kept — a name derived from bytes that do not change need
not be derived twice — and the URL is served with a year and `immutable`. Edit the
file under a running process and the page goes on linking the old name, which the
browser was told can never change, so the edit is invisible until a restart. Not an
error: a page that simply did not change.

So with `DevMode` on, every mount re-reads the hash and serves `no-store`. An edited
file gets a new URL, the page links it, and the browser fetches it.

That is half the problem. The other half belongs to the application: a mount over an
`embed.FS` serves bytes that were fixed when the binary was built, and no amount of
re-hashing changes them. Serve the directory in development and the embedded copy
otherwise — which is what `collage new` scaffolds:

```go
func staticFiles(devMode bool) (fs.FS, error) {
	if devMode {
		if root, err := os.OpenRoot("static"); err == nil {
			return root.FS(), nil
		}
	}
	return fs.Sub(staticFS, "static")
}
```

collage does this for templates itself, because `Template.Root` already tells it
where they are. A mount is handed an `fs.FS` and nothing else, so where the files
came from is something only the application knows.

A static build writes both: each file under its own name, and a content-addressed
copy of every file something actually linked. Only those — the mount records each
URL as it is minted, so a media directory is not doubled for the sake of names no
page uses.


## Options

```go
err := app.Mount("/media/", root.FS(),
	collage.WithCacheControl("public, max-age=31536000, immutable"),
	collage.WithoutBuildCopy(),
)
```

| Option | Effect |
| --- | --- |
| `collage.WithCacheControl(value)` | The `Cache-Control` served with every file in this mount. The default is `public, max-age=3600` |
| `collage.WithoutBuildCopy()` | A static build does not copy this mount into its output |

`Cache-Control` is the one part of an asset response the framework does not
derive, because it cannot: how long a client may keep a file depends on whether
its name changes when its content does. A year and `immutable` is right for
fingerprinted filenames and wrong for `app.css`.

`WithoutBuildCopy()` is for a mount served from a CDN in production, or one large
enough that duplicating it into the build directory is not wanted.

## What a mounted file response contains

The handler is a thin layer over `fs.Open` and `http.ServeContent`, not
`http.FileServer`. `ServeContent` is what provides `Range`, `If-Range`, `206` and
`Last-Modified` — which is what makes seeking in an audio or video file work at
all — and it is the reason assets are a separate mechanism from
[documents](documents.md) rather than a `[]byte`-returning handler.

`http.FileServer` was deliberately not used: it serves directory listings (an
information leak), imposes its own `/index.html` → `/` redirect, cannot take a
per-mount `Cache-Control`, and answers 404 in HTML.

| | |
| --- | --- |
| Methods | `GET` and `HEAD`. Anything else is `405` with an `Allow: GET, HEAD` header |
| `Content-Type` | From `mime.TypeByExtension`, falling back to `ServeContent`'s 512-byte content sniff |
| `ETag` | A strong content hash, computed on first serve and memoised |
| `Cache-Control` | The mount's, on every response |
| Directory listings | None. A request for a directory, or for the bare prefix, is a 404 |
| Implicit `index.html` | None |
| A missing file | `404` in `text/plain`, with `no-store` and `nosniff` — never HTML |

### The ETag is a content hash, and that is not an arbitrary choice

**`embed.FS` reports a zero `ModTime` for every file.** Size-and-mtime validation
therefore fails silently for the most common asset source in a Go program — and
so does `Last-Modified`, which `ServeContent` omits for a zero time.

So the framework hashes instead. The hash is computed lazily, on the first serve
of each file, and **only the hash is memoised, never the body**: the memory cost
is bounded by file count, not by total asset bytes. Hashing the whole file system
at mount time was rejected for the opposite reason — it would make startup
proportional to how many bytes you serve.

A consequence worth knowing: a file whose content changes on disk keeps its
memoised ETag for the life of the process — outside development, where nothing is
remembered (see above). Mounts are meant for content that is deployed, not edited
under a running server: restart after replacing a file.

## Assets never enter the page cache

This is the entire reason assets are a separate mechanism, and it is worth being
explicit about what follows from it.

A mounted file is not stored by `collage.Cache`, is not keyed by the page cache
key, is not tagged, and is not reached by `InvalidateTags`. `MaxEntries` does not
apply to it, and it cannot evict a page.

The page cache is in memory and bounded by *entry count*, not by bytes. One 50 MB
zip file stored in it would evict thousands of pages; and computing a page's
content-hash ETag over 500 MB on every request is not viable. Both problems
disappear when files are served as files.

**The freshness of a mounted file is the mount's `Cache-Control` and the client's
business, not the framework's.** There is no server-side expiry to tune and no
invalidation call to make. If a file's content changes and its URL does not,
every client that cached it keeps the old one until its `max-age` elapses. That
is the trade content-addressed URLs exist to solve — link the file with
`{{asset}}` and a changed file is a new URL (see above). A file linked by its plain
name gets only the mount's `Cache-Control`.

## Prefixes, shadowing and ordering

A mount prefix must begin and end with `/`, and must not be `/` alone — a mount
at the root would swallow every route. `collage.ErrInvalidPrefix` otherwise, and
`collage.ErrNilFS` for a nil file system.

Mounts are matched **by prefix, before routing**. A request whose path starts
with a mount's prefix is answered by that mount, full stop: it never reaches the
page router, which is why a missing stylesheet gets a plain-text 404 rather than
your site-wide HTML not-found page.

That is only safe because of a startup check. A mount prefix that would shadow a
URL path the router already answers to is refused with
`collage.ErrMountShadowsRoute`, naming both, and two mounts with overlapping
prefixes are refused with `collage.ErrMountConflict`. Both checks run once
registration closes rather than inside `Mount`, so **registration order does not
matter**: a mount registered before the page it would shadow fails exactly as
loudly as one registered after it.

"URL path the router already answers to" is deliberately wider than "page or
document path". It covers all three registries — a page path, a document path,
and a registered `Redirect.From` — because a redirect colliding with a route is a
startup error in either order everywhere else, and a mount is not an exception.
It also covers each route's **locale-prefixed** URL: a page registered at
`Paths{"tr": "/about"}` is reached at `/tr/about`, so a mount at `/tr/` is
refused even though the pattern alone shows no `/tr`.

What the check establishes is narrower than "the mount owns URL space no route
answers to", and the difference is worth knowing: it compares prefixes against
registered patterns, so it cannot see a *dynamic* pattern registered above the
prefix. A catch-all at `/{rest...}` would have matched URLs under `/static/`, and
mounting that prefix takes them. That is the documented trade of claiming a
prefix, not a silent shadowing of a route you named.

Because those checks run when the handler is built, they surface the way every
other startup failure does: `App.Handler()` returns a handler that answers `503`
and logs the reason once, and `App.ListenAndServe()` returns the error instead.
`Mount` itself returns `collage.ErrAppStarted` once the application has started,
like every other registration method.

`App.Mounts()` returns every mount, in registration order, as a copy of the
slice — appending to or reordering it does not affect the application. Its
element type is `*collage.Mount`.

## Path resolution

After the prefix is stripped, the remainder is `path.Clean`ed and checked with
`fs.ValidPath`, which rejects `..`, absolute paths and empty elements. A path
that survives is passed to the `fs.FS`'s `Open`; a directory is refused.

Containment is not treated as a string problem — but, as the warning above says,
what `fs.ValidPath` guarantees is that the *URL* does not express a traversal. It
says nothing about what the `fs.FS` does with a valid-looking name, which is why
the choice between `os.OpenRoot` and `os.DirFS` is yours to make correctly.

## Static builds

A static build copies every mount into its output, under the prefix the mount is
registered at: `/static/app.css` becomes `<OutDir>/static/app.css`. Copying is
the default; `collage.WithoutBuildCopy()` turns it off for one mount.

A mount's `fs.FS` is your code, so every copied file's destination goes through
the same containment checks a rendered page's does — `filepath.Rel`, plus a walk
of every path component looking for a symlink that leaves `OutDir` — rather than
a second implementation of that logic. A failure copying one file is recorded and
the rest of the build continues.

Which mounts a build copies is answered by the application's own `Mounts()`.
There is no `BuildOptions` field for it, deliberately: a caller cannot hand the
builder one application's pages alongside another application's assets.

## Limitations

Stated plainly, because each one is a real constraint rather than an oversight:

- **`os.DirFS` does not enforce containment against symlinks.** Repeated here
  because it is the one that matters. Use `os.OpenRoot`.
- **The source must satisfy `fs.FS`.** S3, GCS and other object stores do not.
  The escape hatch is to compose your own handler around `app.Handler()` at the
  mux level, which has always worked — it simply integrates with nothing: not the
  startup shadow check, not the static build.
- **Content-addressed URLs are opt-in, per reference.** `{{asset}}`,
  `{{stylesheet}}` and `rc.Asset` mint them; a plain `/static/app.css` written into
  a template, or a URL inside a stylesheet (`url(...)` in CSS), is served as it is
  and is not rewritten.
- **No compression.** No gzip or Brotli negotiation, and no pre-compressed
  `.gz`/`.br` sidecar lookup. Put a reverse proxy or a CDN in front, or wrap
  `app.Handler()`.
- **No image processing, no bundling, no minification.** A mount serves the bytes
  it is given.
- **A memoised ETag does not notice a file changing under a running process.**
  See above.
- **Mounted assets are invisible to tag invalidation and to the page cache.**
  That is the design, not a gap — but it does mean `InvalidateTags` can never
  make a client re-fetch a stylesheet.
- **A mount dispatches only `OnError`, and only for a 4xx, a 5xx, or a panic
  recovered while serving it.** `Metrics.HTTPResponse` and the request's trace
  span fire unconditionally, for every asset request, exactly as they do for a
  page or a document — there is no metrics or tracing exemption left to claim.
  What genuinely does not fire for a mount is the render-hook trio,
  `OnPageResolved`, `OnBeforeRender`, and `OnAfterRender`: an asset is not a
  render, so there is nothing for those three to observe. See
  `docs/plugins.md`'s "Documents dispatch four hooks, not seven" section, which
  covers the asset case alongside the document one.

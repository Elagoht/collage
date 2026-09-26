# Changelog

## v0.18.0

### Added

- **`{{fragmentURL "page" "fragment" ...}}`, `{{fragmentURLIn "tr" ...}}` and
  `App.FragmentURL(page, fragment, locale, params)`**: the path a page opened for a
  fragment with `WithFragmentPath`, built by name as `pageURL` builds a page's. As
  strict: an unknown page, a fragment the page did not open
  (`ErrUnknownFragmentPath`), one opened at two paths in a locale
  (`ErrAmbiguousFragmentPath`), a locale with no path, or parameters that do not
  fill the pattern fail it.
- **What a fragment path's fragment hoists reaches the client.** Declarations for
  an area the fragment placed no marker for come ahead of the markup, one inert
  `<template data-collage-hoist="area" data-collage-key="key">` per item, so a
  script can add a stylesheet the page did not load the first time.
- **`ETag` and `304` on a fragment read.** A GET answered with a fragment — every
  fragment path — carries the hash of the body as sent, and `Cache-Control:
  private, no-cache` unless the handler set its own; a matching `If-None-Match` is
  answered `304` with no body.
- **`Host.Handle(prefix, handler)`**: a plugin serves an `http.Handler`, as
  `App.Handle` does — for an event stream or a WebSocket.
- **`Host.RenderFragment(r, FragmentRequest)` and `App.RenderFragment`**: a
  fragment a page opened at its own URL, rendered for a request and returned in
  parts — `HTML`, `Head` (the hoisted items), `DependencyTags`, `Shared` (one
  render serves every reader) and the forgery `Cookie` its forms need. For a
  plugin pushing fragments over a connection it owns. A request names the
  fragment by page and name, or by `Path`, the URL a page linked, resolved as a
  request to it would be. `FragmentRequest`, `FragmentRender` and `HoistItem` are
  exported.
- **`StreamCloser`**: a plugin serving connections that never end by themselves
  implements `CloseStreams`, which runs when shutdown begins, before the server
  waits for open requests.
- **The development reload script can be left out.** It does not connect in a
  browser that sets `navigator.webdriver`, and `?collage-reload=0` serves a page
  without it — for headless `--screenshot` and `--dump-dom`, which waited forever
  on the open stream.

### Changed

- **Breaking:** an action answering GET or HEAD on the path of a page or document
  is refused with `ErrDuplicateRoute`, in either registration order. It used to
  be matched first and hide the page: a fragment path spelled like a page's path
  served the fragment in the page's place, with no error.
- **Breaking:** `plugin.Host` has two new methods, `Handle` and `RenderFragment`;
  a test double implementing it needs them.
- `render.Engine.RenderFragment` returns the body with the hoist channel;
  `SlotEngine.RenderFragmentResult` returns the parts.

### Fixed

- A handler mounted with `App.Handle` can reach the connection through
  `http.ResponseController`: `SetWriteDeadline`, `Flush` and `Hijack` pass the
  wrapper that records its status. A stream could not push its deadline forward
  and was cut by `WriteTimeout`, and a WebSocket upgrade failed with 501.

### Documentation

- `DataHandlerFunc`, `WithDataHandler` and the fragment path section say when to
  reach for `Once` and when for `Cached`: every fragment path is a render of its
  own, so fragments refreshed separately share a fetch only through `Cached`. The
  demo scaffold's counter reads through `Cached`.

## v0.17.0

### Added

- **`FragmentBuilder.Static()`**: a fragment states that its data handler returns
  the same for every request to one URL — it reads the path's parameters and the
  locale, and nothing else a request carries — so it does not make a page that
  declares no strategy dynamic. For a fragment many pages share whose data is
  fixed per URL, so each page using it need not say `Static()` itself. It covers
  the fragment's own handler only: a slot resolver still makes a page dynamic, and
  a page's declared strategy is kept.

## v0.16.0

### Added

- **A page that declares no strategy is static unless something in it fetches.**
  Registration resolves it: dynamic when any fragment it renders — layout,
  content, slot fills, fallbacks, fragments opened with `WithFragmentPath` — has a
  data handler or a slot resolver, static otherwise. A site of pages without
  handlers is cached and exported without `Static()` on every page. A declared
  strategy is kept as it is. `StrategyAuto` is the value before registration.
- **`FragmentBuilder.WithData(v)`**: fixed data for the template, without a
  handler, so it leaves the page static.
- **`FragmentBuilder.WithTitle(s)`**: the page's `<title>` without a handler. The
  innermost declaration still wins, and a handler of the same fragment hoisting a
  title replaces it.
- **`DocumentBuilder.WithBody(b)`**: a fixed body in place of a handler. A document
  declaring no strategy is static with a body and dynamic with a handler.
- **Slots need no declaring.** A template calling `{{slot "aside"}}` is the
  declaration: `WithSlotFragment` and `WithSlotResolver` bind into a slot nothing
  declared, as an optional one holding any number of fragments, and a layout needs
  no `WithSlot("content", ...)`. `WithSlot` remains for a required or single slot,
  before or after the bindings it constrains.
- **`WithStaticParams(fn)`** on pages and documents: the placeholder values a
  static build writes a `{param}` pattern for, one map per file, per locale —
  every post of a blog at `/blog/{slug}`. The build makes each path as a link
  built by name would, prefix included, writes it at its decoded path, and hands
  the values to the handlers through `rc.Param`. A map that does not fill the
  pattern exactly fails that file with `ErrRouteParams`; an error or a panic in
  the function fails that locale. Only a build calls it.
- `collage.ErrConflictingData`: a fragment with both `WithData` and
  `WithDataHandler`, or a document with both `WithBody` and `WithHandler`, is
  refused at registration.

### Changed

- **Breaking:** a page with no strategy and no data handler is now static — cached
  when the cache is on, served with `public, max-age=0, must-revalidate`, and
  exported. Add `Dynamic()` to keep one rendering per request.
- **Breaking:** `collage.Data(v)` is removed; `WithData(v)` replaces it.
- **Breaking:** `BuildOptions.PathProvider` and `BuildOptions.DocumentPathProvider`,
  and the `PathProvider`, `DocumentPathProvider` and `PathInstance` types, are
  removed; `WithStaticParams` replaces them, beside the route it expands.
- **Breaking:** the slot check at registration faces the other way. A fragment
  bound into a slot its template never calls fails with `ErrUnknownSlot`, naming
  the slot and the calls the template does make; a template calling a slot
  nothing declares or fills renders it empty, where it used to fail registration
  and render with `ErrUnknownSlot`. `Fragment.Bind` declares the slot it binds
  into rather than returning `ErrUnknownSlot`.
- The documentation writes a data handler as a function of `WithDataHandler`'s own
  shape, returning `any`, and keeps `collage.DataHandler` and `collage.Load` for a
  loader that is also called where its concrete type matters — a test, another
  handler.
- The scaffolded layout names the site with `WithTitle` and declares no slots; the
  home page no longer says `Static()`, and hands its template the project's name
  with `WithData`.

## v0.15.0

### Added

- **`collage dev` shows errors in the browser.** It listens on `HOST` and `PORT`
  itself and passes requests on to the program, which it runs on a loopback
  address of its own. When the program exits — a template that is not found at
  registration, a panic at startup — or the first build fails, the page is a 503
  with what it printed, instead of a refused connection; a page already open
  reloads onto it, and reloads again once a change brings the program back. A
  request made while the program starts waits for it.
- **A template calling a slot its fragment does not declare fails registration.**
  `{{slot "aside"}}` in a fragment declaring only `more` used to be a 500 on every
  request that rendered it; `RegisterPage` now returns `ErrUnknownSlot`, naming the
  page, the fragment, the template and the slot. Calls in included templates and
  defined blocks count; a slot named by a non-literal, `{{slot .Which}}`, is left
  to the render.
- **The development 500 page leads with the cause.** A render failure's message is
  every template it passed through, outermost first, on one line; the page now puts
  the template, line and column of the call that failed, and what it returned,
  above that chain, and is styled like the rest of collage's development pages.
  The production page is unchanged.

### Changed

- The program `collage dev` runs is given `HOST` and `PORT`, after the environment
  file's, and must listen there: the scaffolded `main.go` does. One that is still
  not listening there 10 seconds after it started is named on the page, with the
  address it was given.

## v0.14.3

### Added

- **`collage.Data(v)`**: a data handler that hands the template `v` on every
  render, for data fixed when the program starts — a list of links, a heading —
  with no function to write:
  `WithDataHandler(collage.Data(homeView{Links: links}))`.
- **`collage.Load(fn)`**: `DataHandler` without the tags, for a handler of shape
  `func(ctx, rc) (T, error)`. A cached page whose data changes still wants
  `DataHandler`, whose tags invalidate it.

### Changed

- The scaffolded home page hands its template the project's name with
  `collage.Data`, and the minimal template's page reads `Hello from {{.Name}}`: a
  first project shows where a template's data comes from. The demo's clock and
  hello page, which report no tags, use `collage.Load`.

## v0.14.2

### Changed

- **`collage new --template demo|minimal`** replaces `-minimal`; `demo` is the
  default, and a template it has no scaffold for is refused naming the ones there
  are. Flags take one dash or two.
- **The minimal template is minimal.** A layout around one page,
  `<h1>Hello from collage</h1>`, and a stylesheet setting the background and text
  colour with a dark mode, beside the `main.go`, `go.mod` and `routes.go` every
  project has. The not-found page, the tests, `.env.example`,
  `plugins-config.json` and the favicon moved to the demo template, which keeps
  them.
- The scaffolds link the documentation site, https://collage.furkanbaytekin.dev,
  instead of the repository's `docs/` directory the demo's "Read the docs" pointed
  at.

## v0.14.1

### Added

- **`DocumentBuilder.AtRoot(pattern)`**: a document outside every locale, at its
  bare path in every configuration and rendered in the default locale — for the
  site's own files, `/robots.txt`, `/llms.txt`, `/.well-known/…`. A link built by
  name reaches it from a page in any language.

### Changed

- **Under `PrefixDefault`, documents are prefixed like pages.** v0.14.0 kept every
  default-locale document at its bare path, generalising from `robots.txt`, which
  is the one file that must be at the root; a sitemap or search index per language
  (`/en/sitemap.xml` beside `/tr/sitemap.xml`) could not be built. A document is
  now at `/en/…`, its bare path redirects there, and one that belongs at the root
  says so with `AtRoot`.

## v0.14.0

### Added

- **`LocaleConfig.PrefixDefault`.** The default locale's pages get a prefix of
  their own, like every other locale's: `/en/about` beside `/tr/hakkinda`, with no
  language at a bare URL. Links built by name carry it; a page requested without
  it, the root included, is redirected (`301`) to the address with it, in one hop
  with the site's trailing slash. Documents keep their default-locale address
  unprefixed — `/robots.txt` and `/sitemap.xml` belong at the root — and
  `/en/sitemap.xml` redirects there. An export writes the default locale's pages
  under `en/`, and at its root an `index.html` that refreshes to `/en/`, names it
  canonical and asks not to be indexed.

### Fixed

- **A redirect to a locale prefix's one spelling keeps the trailing slash.**
  `/TR/hakkinda/` went to `/tr/hakkinda`, dropping a slash a `TrailingSlash` site
  then redirected back: two hops for one address.
- A static build follows only the router's own redirects to a route's spelling —
  its locale prefix, its slash — when rendering by path, never a `Redirect` the
  application registered.

## v0.13.0

Found by the documentation site going bilingual and checking its canonical links
against the live host.

### Added

- **`Config.TrailingSlash`.** On, every page's URL ends in `/` — `/blog/hello/`,
  and a locale's home `/tr/` — in every link built by name (`pageURL`, `pageURLIn`,
  `localeURL`, `App.URL`), and a request without the slash is redirected to it. An
  export writes a page as `<path>/index.html`, which a static host serves at the
  slashed address and reaches from the other only through a redirect; without this,
  a static site's every canonical link, sitemap entry and internal link pointed at a
  redirect, and collage had no way to build any other URL. Documents keep their
  paths as written; an action is answered at either spelling.

### Changed

- **Each page has one spelling of its trailing slash.** A page used to answer both
  `/about` and `/about/` with a `200`, two addresses for one page in every cache and
  every search index. The spelling the site does not use now redirects, `301`, with
  its query string: `/about/` to `/about` by default, the reverse with
  `TrailingSlash`. The root is `/` in both.
- A static build renders a page at the address it is answered at, so a
  `PathProvider` returning `/blog/hello` builds the page a `TrailingSlash` site
  serves at `/blog/hello/` rather than failing on the redirect.

## v0.12.0

### Changed

- **A page's head is the same on every render.** A hoisted key used to be written
  where its first declaration *arrived*, and sibling fragments' handlers run
  concurrently, so two siblings' stylesheets or meta tags could swap places from one
  request to the next — the cascade included. A key is now placed at its earliest
  declaration in the tree's own order: the declaring fragment's position, then the
  order that fragment declared in. Which declaration wins a key is unchanged.

### Fixed

- **Concurrent misses on a document are coalesced**, as a page's are: an expiring
  feed polled by many clients runs its handler once, not once per client.
- **`RegisterPlugin` after any failed start is `ErrAppStarted`**, including a start
  refused for a plugin configuration key no plugin claims, which runs before any
  plugin's `Init`.

## v0.11.1

- The Dockerfile `collage build -i` writes copies `plugins-config.json` when the
  project has one. The binary reads it from its working directory, and a container
  without it ran every plugin on its defaults without a word.
- `ErrVaryOutsideRequest` names `SkipCache` too; the path providers' comments say
  each path is checked before its own file is written.

## v0.11.0

Found auditing the documentation site against the source.

### Changed

- **A placeholder inside a path segment is refused at registration.**
  `/feeds/{category}.xml` used to register as literal text: the route matched only
  the braces themselves, and every real URL was a 404. A placeholder is a whole
  segment — `/feeds/{category}/rss.xml`.
- **`Vary` and `SkipCache` close when routing begins, on every route.** Called from
  a data handler they returned `ErrVaryTooLate` on a cached page and silently did
  nothing on any other; they are now an error everywhere.
- **A disk cache directory that cannot be created falls back to memory**, with a
  warning, rather than stopping the application from starting.

### Fixed

- **A fragment opened with `WithFragmentPath` is checked at registration** like the
  rest of its page — its template, its builder's mistakes, its validation. One with
  a missing template answered with an empty 200.
- **A 405 on a document's URL is plain text**, as every other document failure is.
- **`RegisterPlugin` after a start that failed in a plugin's `Init` returns
  `ErrAppStarted`**, not an unexported error.
- **One `ErrNoActionHandler`** for registration and for a request.
- **An action exempted with `WithoutCSRF`** no longer triggers the missing-key
  warning.
- `collage -h` exits 0; `collage help nope` no longer doubles its prefix.

## v0.10.0

Found writing the documentation site against the source.

### Changed

- **The disk cache's namespace is the build alone.** It included the forgery key,
  which emptied the cache on every key change — and on every restart of a site with
  no `Security.CSRFKey`, forms or not, since a generated key differs every run. A
  stored page carrying another key's marker is now a miss instead, rendered again;
  a page without a form survives. The startup warning that a missing key starts the
  disk cache empty is gone with it.
- **One URL per page per locale.** `/en/about`, the default locale's own prefix, and
  `/TR/hakkinda`, another spelling of a supported one, redirect permanently to
  `/about` and `/tr/hakkinda` — `301`, or `308` for anything but `GET` and `HEAD`,
  with the query kept. They were second URLs for one page.
- **A document in a non-default locale is exported under its prefix**:
  `<OutDir>/tr/feed.xml`, as it is served. It was written to the bare path — a 404 on
  a static host — and one pattern in two locales collided, one of them skipped.
- **A builder's mistakes are refused at registration** whether or not anyone called
  `BuildErr`. A slot declared twice or a fragment bound to an undeclared slot,
  anywhere in the tree, now fails `RegisterPage` by name; `RegisterDocument` does the
  same for a document. A page's own action without a handler, or with a name
  already taken, is refused there too rather than on its first request; a fragment
  a slot resolver returns is checked when it first renders.

### Added

- **`Config.DevWatch`** names directories, besides the templates and the mounts,
  whose changes reload a development page — content an application reads from disk,
  such as Markdown. Found building the documentation site, whose pages are Markdown
  and did not reload when edited.
- **`rc.HoistAlternate(hreflang, href)`**, one `<link rel="alternate">` per
  language, for a page's translations.
- **`SkipRecord.Err`** carries the skip's sentinel — `ErrNotStatic`,
  `ErrDynamicPathUnresolved`, `ErrUnresolvedToken`, `ErrDuplicateOutputPath` — for a
  caller that acts on the kind of skip rather than reading the sentence.
- **More sentinels exported** for `errors.Is`: `ErrNotStatic`, `ErrUnknownAsset`, the action
  registration errors, `ErrCSRFMissing`, `ErrCSRFMismatch`, `ErrCSRFInvalid`,
  `ErrCSRFDisabled`, `ErrDictOddArgs`, `ErrDictKeyNotString`, `ErrMethodNotAllowed`,
  `ErrNoMountForAsset` and `ErrTemplateEscapesRoot`. Serving and building share one
  `ErrEmptyRender`.
- **A plugin's commands reach a scaffolded project.** The `collage` binary never
  loads a project's plugins, and the scaffolded `main.go` never called
  `DispatchCommands`; it now does, with the first word after its flags —
  `go run . <command>`.

### Fixed

- **A page an action answers with runs the render hooks**, so a validation page is
  minified and annotated like any other. A document's handler gets `collage.Cached`
  and `rc.Asset`, which were `Once` and an error there.
- **`Security.CSRFFieldName` renames the field `{{csrfToken}}` renders**, not only
  the one the verifier reads — a form using `{{csrfToken}}` under a renamed field
  was refused on submission.
- **A form over the body limit is a 413**, not a 403, with forgery protection on:
  the body's size error was discarded for a body that is not multipart.
- **The development 500 page names the fragment where a failure started**, not the
  layout it passed through on the way up.
- **A document that reads the query gets the export warning** a page does.
- **A scaffolded page's own title replaces the site's.** The layouts wrote a literal
  `<title>` beside `{{hoist "head"}}`, so a page calling `rc.HoistTitle` had two; the
  layout now declares the site's name with `HoistTitle`, which a page's declaration
  replaces.

### Docs

- The reference notes and doc comments caught up with the code: the disk cache and
  `Cache.Type`, template functions rebound per render, `Template.FS` in development,
  every method of `Host`, the hooks that error pages, actions, documents and static
  builds run, the CLI's flags and commands, the exported paths of other locales, the
  Dockerfile `collage build -i` writes, and which scaffold has `/healthz`.

## v0.9.0

- **`collage.Cached` keeps data across pages and requests.** The page cache stores
  what a render produced, so thirty pages showing two authors fetched the authors
  thirty times — and an export always did. `Cached(rc, key, ttl, tags, fetch)` stores
  the value itself: those pages now fetch twice, served or exported. Its tags join
  the page's, so one `InvalidateTags` replaces the value and every page built from
  it. Concurrent requests for a key share one fetch; errors are not stored; values
  are in-process and bounded by `Cache.MaxEntries`. In development, a preview, or
  with the cache off, it is `Once`. See
  [docs/caching.md](docs/caching.md#caching-data-not-only-pages).

## v0.8.0

- **A development page with a broken part says so.** A fragment that failed — even
  one a fallback covered — puts a panel on top of the page naming it and its error,
  with the template's file and line; your own 500 page gets the same panel with the
  failure behind it. Before, the error was in an HTML comment, or nowhere. Only in
  development, and never cached.
- **`collage.SkipCache(r)`**, from middleware, answers a request with a fresh render
  that is neither read from the cache nor written to it — what a preview of a draft
  needs. [docs/http.md](docs/http.md) has a complete preview with a signed cookie.
- **`collage.Get[T](rc, key)`** reads shared data as the type it was stored as.
- **An action answering with an unregistered page is refused by name**
  (`ErrUnregisteredPage`) instead of rendering a layout around nothing.
- **Why a page is not streamed**, and what to do about a slow part instead, is in
  [docs/architecture.md](docs/architecture.md).

## v0.7.1

- A development page no longer misses a change made the moment it connects. The
  watcher took its first look from inside the goroutine it started, so a file
  saved before that look was part of the starting point rather than a change.
  Found by CI, which caught the race on its first run.

## v0.7.0

Found moving a real site onto collage.

### Changed

- **A served error page runs the render hooks.** It skipped `BeforeRender` and
  `AfterRender`, so the 404 a server answered with was not minified, carried no
  structured data and had no images rewritten — while the `404.html` an export
  wrote had all three.
- **A 404 from missing content is logged at debug**, like a route miss. A data
  handler returning `collage.ErrNotFound` is an answer, and every bot requesting an
  unknown slug was an ERROR line. It still reaches `OnError` as `ErrNotFound`.

### Added

- **Slots filled per render.** `WithSlotResolver(name, func(rc) ([]*Fragment, error))`
  decides a slot's fragments per request, after its own fragment's data handler and
  before theirs — for sections that come from content, reordered with no restart.
- **Head helpers that escape.** `rc.HoistTitle`, `HoistMeta`, `HoistProperty`,
  `HoistLink` and `HoistStylesheet`, and `{{stylesheet "/static/x.css"}}` in a
  template, which hoists a fragment's own stylesheet into the head through its
  content-addressed URL, once however many fragments ask. `rc.Asset(path)` is that URL
  in Go.
- **Export warnings.** A page that reads query parameters (`WithCacheParams`) is
  written without them, and the report now says so: a static host answers
  `/blogs?page=2` with the `/blogs` file.
- **`collage.Effect`** adapts a data handler that only declares things for the page,
  and **`collage.JSONOf(status, v)`** marshals a JSON action's answer.

### Docs

- The disk cache's namespace includes the forgery key, and everything sharing a
  `Dir`, a build and a key shares entries — two `App`s in one process included. The
  scaffolded tests now give each application its own cache directory, so a test no
  longer reads what the previous one rendered.

## v0.6.0

- **Links by name.** `{{pageURL "blog-post" "slug" .Slug}}` builds a page's or a
  document's URL in the render's locale, falling back to the default locale for a
  page with no path in this one; `{{pageURLIn "tr" "about"}}` asks for one locale
  exactly; `{{localeURL "tr"}}` is the current page in another language, empty
  when it has not been translated — a language switcher in two lines. From Go it is
  `app.URL(name, locale, params)`. A link that cannot be built — an unknown name, a
  missing or extra parameter, a `..` — fails the render rather than shipping a
  404. See [docs/fragments.md](docs/fragments.md#links-by-name).
- **The browser reloads itself in development.** A page served in development
  reloads when a template or a mounted file changes, and when the program comes
  back from a rebuild, over a stream at `/_collage/reload` that exists only in
  development. The answer to a POST never carries it, and shutdown closes the
  streams rather than waiting on them.
- **`collage new -minimal`** scaffolds one layout, an empty home page and a
  not-found page, for starting a real site without deleting the demos. The
  scaffold's routes now live in `routes.go`, and `main.go` is shared by both.

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

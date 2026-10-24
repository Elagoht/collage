# Changelog

## v0.75.0

### Changed

- **`@blog` no longer adds elagoht/markdown or elagoht/highlight.** It is
  `@site` with feed, toc and ogimage; a blog whose posts are Markdown files, or
  that shows code, adds them itself:
  `collage plugin add elagoht/markdown elagoht/highlight`.

### Fixed

- A forgery-token test failed about one run in four: it tampered with a masked
  token's last character, whose low bits are padding the decoder drops.

## v0.74.0

From the system monitor trial.

### Changed

- **`collage add guard` writes the guard function alone**, `guards/<name>.go`.
  It no longer writes `fragments/layouts/<name>.go`, a layout that only carried
  the guard: a guard is a function, and `--guard` puts it where it is asked.
- **`--guard <name>` adds `.WithGuard(guards.<Name>)` and nothing else.** On
  `collage add page` it goes on the page's content fragment instead of wrapping
  the page in the guard's layout; the page's renders, its actions and its
  fragment paths meet the guard as before. `collage add layout` takes `--guard`
  too, and the layout carries it. `--guard` on a page or a layout is refused,
  before anything is written, when `guards/` declares no such function. Layouts
  the old `add guard` wrote keep working.

### Fixed

- A copied mount file whose close failed — where a deferred write such as a full
  disk shows up — was reported as copied. The dev reload stream kept a
  connection whose writes failed until the next flush.

## v0.73.0

From the system monitor trial.

### Added

- **`collage.URL(ctx, name, params)`** builds the path of a page or document by
  its name for code that runs in a request but has no `RenderContext` — a guard,
  a middleware. It is `rc.URL`'s twin: in the locale the request resolved to,
  falling back to the default one as `{{pageURL}}` does, and as strict. A
  middleware runs before routing, so it builds in the default locale; outside a
  request collage serves it is `ErrUnknownRoute`.
- **`collage.GuardSeeOtherPage(ctx, name, params)`** is a guard's redirect to a
  page by its name, what `SeeOtherPage` is to an action: a guard on `/tr/panel`
  sends the reader to the Turkish login rather than a path written into it.

### Changed

- **`collage add page` and `collage add fragment` write the data handler as a
  function that makes it:** `WithData(aboutData())`, with `aboutData()
  collage.Data` returning the `collage.Load(...)`. A service the handler reads
  through is then a parameter added to `aboutData` and passed on by `About`,
  rather than the handler rewritten into that shape. What `collage inspect`
  reports, and the template check, are unchanged.
- **`collage add guard`** shows `collage.GuardSeeOtherPage(ctx, "login", nil)` as
  the way to redirect, instead of a hard-coded `Location`.

## v0.72.0

### Security

- **collage needs Go 1.26.10 or later.** `go.mod` says `go 1.26.10`, and so does
  the one `collage new` writes. Go 1.26.10 rejects the compromised eMudhra
  intermediate certificates (CVE-2026-111943), and Go 1.26.9 before it fixed
  net/http, mime/multipart, crypto/tls and html/template. A program carries the
  standard library it was built with, so a rebuild with an older Go shipped those
  holes again. With `GOTOOLCHAIN=auto`, the default, the go command now fetches
  1.26.10 for a build that would have used an older one; with
  `GOTOOLCHAIN=local` and an older Go, the build stops and says why. `go get
  github.com/Elagoht/collage@v0.72.0` raises an application's own `go` line to
  match.
- **The forgery token is masked anew in every response.** A response that
  reflects what a request sent and is compressed on its way out — by
  collage-compress, a proxy, a CDN — is shorter when the reflected text matches
  part of the token on the same page, and an attacker who can make a reader's
  browser send requests could read the token from those lengths (BREACH). The
  form field now holds the token XORed with a fresh random pad, prefixed `~`, so
  it reads differently every time; the forgery check takes the mask off, from
  the form and from the header. A token submitted unmasked — a form served
  before this release — is still accepted. A page's `ETag` names the body with
  the token itself, so a conditional request is still answered `304`.

## v0.71.2

### Security

- **A path segment holding a NUL matches no route.** A captured value is what a
  handler joins onto a directory, and a NUL ends the name at the system call:
  `/docs/a%00b.md` against `/docs/{slug}.md` captured `a\x00b`. A backslash is
  still a value like any other — it round-trips through a link and a build — so
  a handler joining one onto a directory on Windows checks it with
  `filepath.IsLocal`.

### Documentation

- `collage.Cached` keys belong to the application, not to raw request input:
  `MaxBytes` does not bound the data store, so a key from a query parameter is an
  entry per value a visitor invents ([caching](docs/caching.md)).
- A bare `app.Handle` gets the encoded-slash 404 only for a path that needed
  cleaning; `/api%2Fx` reaches it with the decoded `/`, as under net/http alone.

## v0.71.1

### Fixed

- **`collage inspect`, `collage routes` and `collage check` no longer wait
  forever on a program that serves instead of answering.** A `main.go` written
  before `collage.Main` that never hands the words after its flags to
  `collage.DispatchCommands` took `collage-inspect` for nothing and started
  serving, and the command waited on that server until it was killed. The CLI
  now sets `COLLAGE_CLI_COMMAND` to the word it asks with, and
  `App.ListenAndServe` refuses to serve while it is set, returning
  `the collage CLI ran this program to answer "collage-inspect", but it started
  serving instead: main must hand the words after its flags to
  collage.DispatchCommands, as collage.Main does`. It takes the project's collage
  at v0.71.1 or later; with an earlier one the command still waits.

## v0.71.0

Security fixes from an audit of every surface a client reaches, after the
2026-10-21 PHP, Node.js, NGINX and Go security releases. Build with Go 1.26.10
or later: it is the release that fixes CVE-2026-111943 (crypto/x509, the
compromised eMudhra intermediates), and Go 1.26.9 fixed net/http,
mime/multipart, crypto/tls and html/template issues before it.

### Security

- **A request body is bounded before anything reads it, middleware or not.**
  Without middleware, nothing bounded the body before a page guard, an action's
  guard or a data handler read it, so a guard calling `FormValue` read past
  `Server.MaxBodyBytes` (4 MiB by default) — 40 MiB in the report. The body is
  now bounded on every route but a bare `Handle`, and before an action's guards.
- **Multipart files spilled to disk are removed.** net/http removes them only for
  the request it made itself, and collage hands every handler a copy. A form
  parsed by a guard, a data handler or a middleware left its temporary files
  behind for good, a refusing guard included: a whole body's worth of disk per
  request, for anyone who sent one. They are removed when the request is
  answered.
- **An action is not run on a form cut down to its limit.** A body whose
  `Content-Length` is over the limit is refused with 413 before the forgery
  check and the handler; a chunked one that runs into the limit is refused
  after the handler rather than answered as a success. A middleware that buffers
  the body and replaces it (`io.ReadAll`, then `io.NopCloser`) no longer hides
  that the body it read was too large.
- **A `Shared` fragment render sees nothing of the reader it is made for.**
  `App.RenderFragment` reported a cacheable page's fragment `Shared` but
  rendered it from the reader's own request, cookies and context values
  included, and collage-live pushed that one render to every reader of the URL:
  one reader's data, and one host's origin, to the others. A render reported
  `Shared` is now made the way the page cache makes its one render, from a
  request with the reader's cookies, headers and context values stripped; a
  request carrying a `collage.Vary` value is rendered per reader.
  `FragmentRender.Shared` now means the same for readers on the same host.
- **`Collage-Location` carries only a web address.** A script that asked with
  `Collage-Fetch` navigates with `location.assign`, which runs a `javascript:`
  URL in the page where a browser following a `Location` refuses to. An action
  redirecting to a `javascript:`, `data:` or other non-http address is now
  answered as the 303 itself, which fetch will not follow.

### Changed

- Page routes are held to `Server.MaxBodyBytes` as actions are, with or without
  middleware. A page has no use for a body; one sent with a GET is read only if
  something reads it.
- A body whose `Content-Length` is over an action's limit is refused before the
  forgery check, where it was refused when the check read past the limit.

## v0.70.1

Only the CLI changed; a project needs no new collage version.

### Fixed

- **`collage inspect`, `collage routes` and `collage check` no longer fill the
  Go build cache.** They ran the project with `go run .`, which stores the
  linked program in the build cache, built without `-tags collage_dev`, so the
  compiled `main` package held every file the scaffold embeds. Each run after a
  change to `main` — adding a route to `routes.go`, say — stored another copy of
  `templates/` and `static/` there, kept for five days: about 70 MB a run with a
  30 MB `static/`. They now build the project as `collage dev` does, into a
  temporary file, and run it with `COLLAGE_DEV=1`, reading `templates/` and
  `static/` from disk: about 1 MB a run. Their output is unchanged; development
  mode also loads the `.env` file and uses an in-memory cache, so a run no
  longer creates `.cache/`.
- **`collage plugin check -run`** writes its program into
  `collage-plugin-check/` rather than a directory with a random name, which the
  go tool took for a new package on every run and stored again, and builds it
  into a temporary file rather than with `go run`. A `collage-plugin-check/`
  already there — another check, or an interrupted one — is refused and left
  alone.
- The CLI's own tests build with `-trimpath`, so a run reuses what the last one
  compiled instead of storing every package again under a new temporary
  directory: a full `go test ./...` added about 174 MB to the build cache, now
  about 1 MB.

## v0.70.0

### Added

- **`ActionBuilder.WithGuard(g)`** gives an action a guard of its own, the same
  `GuardFunc` a layout carries. It is asked after routing and before the body is
  read or the forgery token checked, so a refused request costs neither. An
  action on its own URL — which has no page, and so had no way to be guarded but
  a middleware or a check in its handler — meets it alone; one on a page's URL
  meets the page's guards first, then its own: the page decides who sees the
  form, the action who may submit it. A nil guard is a build error. `collage
  inspect` reports it as an action's `"guarded": true`.
- **`collage add action <[area/]name> --guard <name>`** writes the action with
  `.WithGuard(guards.<Name>)`, refusing a guard `guards/` does not declare.
  **`collage routes`** says which actions have a guard of their own.

## v0.69.0

From the system monitor trial: what `collage add` could not write yet. Only the
CLI changed; a project needs no new collage version to use what it writes.

### Added

- **`collage add guard <name>`** writes `guards/<name>.go`, a guard that refuses
  everyone with `403` until its check is written, and
  `fragments/layouts/<name>.go`, a layout that adds nothing but the guard.
  **`add page --guard <name>`** wraps the new page in it, inside the site's own
  layout, so the page, its attached actions and its fragment paths meet the
  guard first. The command reminds you that an action with its own `--path` has
  no page and so no guard.
- **`collage add layout <name>`** writes a layout that nests inside the site's
  own around a page's content; **`add page --layout <name>`** wraps a page in it.
  Generated nested layouts carry `//collage:nested`, and a layout with a guard
  counts as nested, so neither is taken for the site's layout a new page goes
  into — which in a project with no `Master` or `Layout` could have wrapped
  every new page in a guard.
- **`collage add middleware <name>`** writes `middleware/<name>.go` and registers
  it with `app.Use`, after the middleware already there.
- **`collage add command <name>`** writes `commands/<name>.go` and registers it
  with `app.RegisterCommand`; `go run . <name>` runs it.
- **`collage add action <[area/]name> --page <[area/]page>`** attaches the action
  to that page's `NewPage` chain instead of printing the line.
- **`collage routes`** lists every URL the project answers through collage —
  pages, the actions on them and on their own URLs, fragment URLs, documents and
  mounts — with how each page is cached and the guards each route meets.

### Fixed

- `collage new -dir .` no longer tells you to `cd .`, and `-force` names the
  files it replaced: `go.mod` among them.
- The scaffolded READMEs describe `collage.Main`, `.env` loading and `collage
  add`'s kinds; they still told `main.go` to parse the flags itself.

## v0.68.0

From building a real site on collage: what was hard to find, hard to read, or
did not start.

### Added

- **`collage.Main(newApp)` runs the program `collage new` scaffolds.** It owns
  `-collage-build`, `-out`, `-clean` and `-port`, a plugin's command after the
  flags, the static build, serving, and the shutdown after a build or a command.
  `newApp` gets a `collage.Env`: `Dev`, `Host`, `Port`, `CacheDir`, and
  `env.PluginConfig(files)` / `env.Static(files, "static")`.
  `PluginConfig` reads `plugins-config.json` from the working directory when it
  is there, as before, and otherwise the copy the binary now embeds, so a binary
  deployed without the file keeps its settings. `Static` serves the static
  directory from disk in development and embedded otherwise. A scaffolded `main.go` is now about fifty lines. An existing
  project keeps working as it is; to move, copy the new `main.go`, `embed.go`
  and `embed_dev.go` from `collage new`, and call `newApp(collage.Env{CacheDir:
  t.TempDir()})` in tests.
- **`collage.Main` loads `.env`.** Before `newApp` runs, the working directory's
  `.env` — `.env.development` first under `COLLAGE_DEV=1`, as `collage dev`
  reads — is put into the environment, so `os.Getenv("BASE_URL")` sees it in
  `go run .`, `collage export` and the built binary with no `source .env`. What
  the environment already sets wins; a malformed line stops the program with the
  file and line.
- **`Host.OpenMounted(urlPath)`** lets a plugin read one of the site's own
  files by the URL it is served at — `/static/icon.png` — through the
  application's mount: from disk in development, embedded in a built binary.
  Only what a request could reach opens. elagoht/favicon reads its source this
  way.
- **`collage.SeeOtherPage(rc, name, params)`** redirects to a page by its name,
  its path built by `rc.URL`.
- **`collage plugin add` says what each plugin needs from you**: after its setup
  note, the template functions it adds and where its documentation is. With
  several plugins — a bundle, `new --with` — they come together at the end.

### Changed

- **`collage add fragment a/b` writes `fragments/a/b.go`**, with its template at
  `a/b` under the template root, instead of under `fragments/pages/`. Its package
  is the directory's, or named after it. A page's content stays in
  `fragments/pages/`.
- **`collage add` names are Go names, kept as written.** `blog/storyDetail` is
  `pages/blog/storyDetail.go`, `StoryDetail()`, the page `"storyDetail"` at
  `/blog/storyDetail`. A dashed name is refused; the directories before it are
  only a path and may still have dashes, `user-settings/profile`, whose package
  drops them.
- **A new project's `files` is the project directory in development**
  (`embed_dev.go`), so a plugin given `files` — ogimage's card templates, a
  favicon source, markdown posts — reads the copy you are editing instead of an
  empty filesystem. Every project now ships `.env.example`.
- **The scaffolded layout is "master" everywhere**: `fragments/layouts/master.go`
  builds `Master()` from `templates/layouts/master.html` (was `main.go` and
  `default.html`). An existing project needs no change.

### Fixed

- **`collage dev` gives the program `BASE_URL`** — its own address — when
  neither the shell nor the environment file sets one. A project made with
  `--with @site` did not start: sitemap and meta refuse to without an origin.

## v0.67.0

### Changed

- **A handler mounted with `App.Handle` runs with no middleware.** Neither the
  application's `app.Use` middleware nor any plugin's (session, flash, i18n,
  consent, secure, ratelimit, basicauth, fail2ban, accesslog, redirects, tenant,
  compress, the development toolbar, …) runs for a request to it any more: the handler is the application's own, and
  wraps itself in the middleware it wants. What still applies is what happens
  before routing: the plugins' `RequestHook`s, the request span, the metrics
  (with `RouteInfo` naming the handler), the panic guard, the clean-path redirect
  and encoded-slash 404, the baseline `X-Content-Type-Options` and
  `X-Frame-Options` headers, and draining on shutdown. Which requests skip the
  middleware is decided on the cleaned path by the same match routing uses, so
  only that handler can be reached that way.

  **An application that relied on `app.Use` or plugin middleware — basicauth,
  ratelimit, session, secure, … — to protect or shape a `Handle` mount must add
  `collage.WithMiddleware()` to that call to keep it:**
  `app.Handle("/api/", api, collage.WithMiddleware())`. Without it, a mount a
  basicauth or rate limit used to guard is now open. A middleware that rewrites a
  request's path into a `Handle` prefix reaches the handler only
  `WithMiddleware()` as well; otherwise the rewritten request is a 404. One that
  rewrites a path *out of* a bare prefix — `/api/legacy` to a page — no longer
  runs for it, since the mount claims the request first.

  No plugin can answer for a bare mount: a ban, an IP allowlist, a maintenance
  page or an access log over the whole site does not cover it. Mount it
  `WithMiddleware()` for that, or put your own middleware around your mux. In
  particular: fail2ban still counts a bare mount's probes in its `RequestHook`,
  but its 403 is middleware, so a banned client keeps full access to `/api/` —
  where login brute force usually happens; accesslog no longer logs its traffic;
  a redirects rule under its prefix no longer fires; and tenant's context is not
  in its requests.

  A plugin's `Host.Handle` is unchanged: a plugin's endpoints (live, oauth,
  ogimage, prometheus, uploads, websocket, …) still run inside the middleware.

### Added

- **`collage.HandleOption`** and **`collage.WithMiddleware()`**, its first
  option. `App.Handle`'s signature gained a variadic `...HandleOption`: existing
  calls compile unchanged (source-compatible), though apidiff reports the
  changed method signature, which a method value or an interface typed
  `func(string, http.Handler) error` no longer matches.

## v0.66.0

### Added

- **`collage.SharedCache`**, an optional extension of `Cache` for a store a fleet
  of instances shares, such as one backed by Redis. When `Cache.Store` is one,
  pages and `collage.Cached` data live in it, with no per-instance copy in front,
  so an invalidation or purge on any instance reaches every instance's next read.
  A write is ordered against invalidations by an epoch the store owns: a render or
  fetch that an invalidation on any instance overlapped is stored stale, and one
  that a purge overlapped is not stored (`WriteFresh`, `WriteStale`,
  `WriteSkipped`). `InvalidateTags` and `PurgeTags` make one `DropTags` call
  (five-second budget), and the pages it reaches, which any instance wrote, count
  toward the result, are named in `CacheInvalidateEvent.Entries` and are warmed,
  within `WarmLimit`. A store that errors or takes longer than 500 milliseconds
  is a miss or a skipped write, never a failed request, and is logged at most
  once every 30 seconds per App, for pages and data together, with no error log
  or `OnError` event per request. A stale write does not replace a fresh entry
  written by a render that began after the invalidation that made it stale. A
  store that is not a `SharedCache`, the memory cache and `Type: "disk"` behave
  as before.
- **`collagetest.SharedCacheSuite`**, which runs the `SharedCache` contract
  against a store through two handles on one backend, and
  **`collagetest.CacheSuite`** for the `Cache`, `TaggedCache` and `StaleCache`
  contracts of any store.
- **`collagetest.NewSharedCache`**, an in-memory `SharedCache`, so a test can run
  two Apps as two instances on one store.

### Fixed

- **A data load that joins a fetch an invalidation overtook gets the value
  stale.** A render that began after an invalidation, and found a `Cached` fetch
  that began before it still running, shared that fetch's value as fresh: its
  page was not degraded, and was stored fresh from the data the invalidation
  replaced, until its TTL, or for good on a `Static` page. The joiner now gets
  the value as the store keeps it, stale, so the render is served `no-store`
  and not stored, as one served a stale value is. This changes the in-memory and
  disk data caches too, not only a `SharedCache`. A load that wants fresh data
  and joins such a fetch now fetches again, as documented.

### Known behaviour

- The instance that invalidates warms for the fleet, within its `WarmLimit`.
- A page invalidated and not yet re-rendered stays in the shared store's tag
  index, so a second invalidation before its re-render counts it, names it in the
  plugin event and offers it for warming again.
- The single fetch per `Cached` key is per instance: N instances missing one key
  at once make up to N fetches.
- A `Cached` value JSON cannot round-trip is not cached across renders in a
  shared store.

## v0.65.0

### Changed

- **The root of a `PrefixDefault` export is the home page.** `dist/index.html`
  is a copy of the default locale's home, with a canonical link to
  `/<default>/` added after `<head>` when the page has none, instead of a page
  that refreshes to it. A visitor to the bare domain on a static host sees the
  site at once, and `BuildFinished` gives it the home page's captured status
  and headers. The server's 301 from `/` is unchanged.

## v0.64.0

### Added

- **Cache warming.** After every `InvalidateTags` and `PurgeTags`, the pages and
  documents the invalidation reached are re-rendered in the background, with
  fresh data, so the next visitor gets a fresh `HIT` instead of paying for the
  render. Four workers, started after the first key warms successfully, as a
  probe. Only variants actually served are warmed: recipes are recorded when an
  entry is written, in memory only, so there are none after a restart until
  pages are written again. A page is warmed again only if a visitor had it
  since its last warm, and an `InvalidateTags` does not bring back a page the
  cache evicted (`PurgeTags` warms its pages all the same). A warm that another
  invalidation overlapped is queued again once no invalidation is under way, at
  most once per overlapping invalidation. A run stops at the first real render
  failure (one warning); skips (404, guarded pages, already fresh, unread,
  evicted) do not stop it. One info line per run. Warm renders skip `App.Use`
  and plugin middleware, request hooks, personalisation, error hooks and
  per-request logs; `BeforeRender`, `AfterRender` and `DocumentRendered` still
  run. Off in development, in static builds and with the cache off; `Shutdown`
  stops it.
- **`CacheConfig.WarmLimit`** caps the keys one invalidation warms, newest first.
  Zero means 1000; negative turns warming off.
- **`collage.CacheWarm`**, a metrics event per warmed key.
- **`collagetest.Site.WaitWarmed`** waits until the warmer is idle, for a test
  that asserts a `HIT` after an invalidation.

### Changed

- **An action's redirect target is warmed before the redirect is written.** An
  action returning `InvalidateTags` and a same-origin `Location` has that
  `Location`'s variants rendered again first, the submitter's own variant first,
  so the redirect lands on the new page. The wait is held to the shortest of 5
  seconds, the request's deadline, and the response's write deadline (the
  action's `BodyTimeout`, or `Server.WriteTimeout`) less 500ms; with no time
  left it does not wait. Past the budget, or on a failure, the redirect still
  goes out and the page is served `STALE`. Redirect to a clean URL or declare
  `CacheParams`: a query that is not a cache dimension of a route declaring none
  is a key of its own, which nothing warms. An action calling `app.InvalidateTags`
  in its handler, rather than returning `InvalidateTags`, gets the background
  warm only.

### Upgrade notes

- A test that asserts `STALE` or `MISS` right after an invalidation races the
  warmer. Set `WarmLimit: -1` in such tests; a test asserting the `HIT` calls
  `site.WaitWarmed()` first.
- Never declare `Cookie` or `Authorization` as a `Vary` header: its value would
  be kept in the warm recipe and sent again with every warm render.
- Warm renders skip middleware, so one that refuses a request (a tenant plugin's
  404 for a deleted host) does not stop warming from writing entries no visitor
  reaches after a purge.
- A key left out of a run (over `WarmLimit`, a stopped run, passed over) is
  untracked until it is next written.
- Expect extra CPU, and a burst of `CacheMiss`/`CacheSet` events and
  `RenderDuration` and fragment metrics, right after large invalidations. Lower
  `WarmLimit` or set it negative to opt out. Declare `CacheParams` on routes
  whose query can carry junk, so junk variants are not keys to warm.

## v0.63.0

### Changed

- **`InvalidateTags` marks pages stale instead of removing them.** On the
  built-in stores (memory, and disk with its new memory tier) an invalidated page
  or document is no longer served as fresh, but its bytes are kept. The next
  request renders it again: a good render is stored and served (`MISS`); a render
  that fails or comes out degraded is not stored, and the stale bytes are served
  instead with `Cache-Control: no-store` and `X-Collage-Cache: STALE`. A render
  answering 404 drops the entry. Before, an invalidation followed by a failing
  render was a 500. An expired page (TTL) is stale in the same sense. There is no
  maximum stale age; eviction at `MaxEntries` or `MaxBytes` bounds it.
  `PurgeTags` still removes outright, and a purged page that fails to render
  fails.
- **A render that overlaps any `InvalidateTags` or `PurgeTags` is not stored**,
  for any tags — one that began while a call was under way, or during which one
  began: it may have read data from before the change.
- **A stale serve is reported once per failure.** The `CacheStale` metrics event
  is counted per stale response served; a failed render that a stale page hides is
  logged once at warn (not also as an error-level `request failed`) and handed to
  the plugins' `OnError` hook once, with a status of 0, however many requests were
  waiting on it. A render degraded only by stale `collage.Cached` data is its data
  refreshing, not an outage: its stale serve is logged at debug.
- **`Cache.Type` `"disk"` is now tiered**: a memory cache (up to 256 MiB, or
  `MaxBytes` when smaller; `MaxEntries` entries) in front of the disk. A hit is served from
  memory, not read from a file. At startup the memory tier is warmed from the
  current build's directory, newest entries first, so a restart serves `HIT`s
  from memory. Files written by v0.62 or earlier carry no key in their header and
  are not warmed; they are still served on the first read.

### Added

- **`collage.StaleCache`**, the optional interface (`GetStale`, `MarkStaleKey`,
  `MarkStale`) a custom `CacheConfig.Store` implements to keep invalidated pages
  and serve them stale.
- **`collage.CacheStatusHeader`** (`X-Collage-Cache`): `HIT`, `MISS` or `STALE`,
  on a cacheable `GET` or `HEAD` that answered with a page or document. Not sent
  in development, on error responses, or in a static build.

### Upgrade notes

- **Memory.** A `"disk"` cache now holds up to 256 MiB of pages in memory as
  well as on disk, or `MaxBytes` when that is smaller. In a small container, set
  `GOMEMLIMIT` and lower `MaxBytes` or `MaxEntries` if needed.
- **Instances sharing one `Cache.Dir` do not share a cache.** An invalidation
  reaches the disk and the invalidating instance's memory; every other instance
  keeps serving its in-memory copy as a fresh `HIT` until it expires — for a
  `Static` page, until a restart or an eviction — and a CDN refetching the page
  meanwhile can keep the old copy. Give each instance its own directory, or use a
  shared `Cache.Store` that implements `StaleCache` and `TaggedCache`.
- **Custom stores.** A `Cache.Store` that does not implement `StaleCache` behaves
  as before: invalidation drops, and it never answers `STALE`. A `StaleCache` that
  is not also a `TaggedCache` cannot have already-invalidated pages purged by tag
  with `PurgeTags`.
- **An action's redirect after `InvalidateTags`**: a page that reads
  `collage.Cached` data is degraded right after the invalidation, so the redirect
  lands on the old page, served `STALE` with `no-store`, while the background fetch
  runs; the next request after it renders and stores the new page. Call `PurgeTags`
  from the handler to show the change at once, failing the page if the upstream is
  down.

## v0.62.0

### Changed

- **`collage.Cached` serves stale data instead of failing.** `InvalidateTags`, and
  a value's `ttl` running out, mark it stale rather than removing it. The next
  render gets the stale value at once and one background fetch replaces it, with
  a fixed 30-second timeout, detached from the request. If that fetch fails, the
  stale value keeps being served, without limit. Before, an invalidation followed
  by a slow or down upstream failed the layout's data, and with it every page.
  A fetch error wrapping `ErrNotFound` still drops the value, so deleted content
  answers 404. A render built from stale data is degraded: served with
  `Cache-Control: no-store` and not cached. `collage.Build` never uses stale
  data, and neither does `App.RenderFragment`, so a plugin pushing re-rendered
  fragments after an invalidation pushes the new value; it waits for the
  upstream, and returns an error when the upstream fails.
- **A failed background refresh cools its key down.** No new refresh starts for
  the key for 1 second after a failure, doubling with each further failure up
  to a minute; a success ends it, and so does an invalidation of the key. A down
  upstream is no longer asked once per request.
- **`AfterRenderEvent.Degraded` is also true for a render built from stale
  cached data**, so plugins see such renders as degraded.
- **`App.Shutdown` waits for the data store**: its running refreshes and its
  disk writes, before the plugins are shut down. Disk writes are now made in the
  background, so a test that points a disk cache at `t.TempDir()` should shut
  the application down in a `t.Cleanup`, as the demo project's `main_test.go`
  now does; otherwise a late write can make the directory's removal fail.

### Added

- **The data store persists beside a disk cache.** With `Cache.Type` `"disk"`,
  `Cached` values are also written to `<Cache.Dir>/data/`, in the background,
  and loaded at startup. A restart serves the persisted data at once and
  refreshes each value in the background on its first use. This directory is not
  namespaced by build, and only the store's own files in it are ever removed.
  Store keys are now stable across processes and change with a type's shape. A
  type JSON would not restore exactly — an unexported or `json:"-"` field, an
  interface — is kept in memory only, unless it implements `json.Marshaler` and
  `json.Unmarshaler`. The files are not encrypted.
- **`App.PurgeTags`** removes pages and `Cached` values outright, from disk too.
- **`CacheStale` metrics event**, reported for each stale value served. A failed
  background refresh is logged at warn, and the first success after failures at
  info.

## v0.61.5

### Changed

- **`collage new` puts the home page at the top of `pages/`, with no `landing`
  area.** The page is `pages/home.go` and its content `fragments/pages/home.go`,
  imported in `routes.go` as `"<module>/pages"` and registered as `pages.Home()`
  — the layout `collage add page <name>` writes for a page with no area. Before,
  they were `pages/landing/home.go` and `fragments/pages/landing/home.go` under a
  `landingpages` import. Existing projects need no change.

## v0.61.4

### Fixed

- **Only plugins whose `Init` succeeded hear of a drain or are asked to close
  their streams.** A `Shutdown` before `Start`, or after a `Start` that failed,
  used to call `OnDrain` and `CloseStreams` on plugins that never ran. A plugin
  reused for a second start — as `collagetest.TrySite` followed by `NewSite`
  does with the same plugin value since v0.61.3 shuts a failed site down — then
  started already draining: collage-health answered `/readyz` with 503.

## v0.61.3

Small fixes from the plugin sealing reviews. No exported name changed.

### Fixed

- **`Start` after `Shutdown` fails with or without plugins.** It failed when a
  plugin was registered and succeeded when none was; it now fails either way,
  and `ListenAndServe` after `Shutdown` still returns nil.
- **`collagetest.TrySite` shuts the application down when building it fails.**
  A failing `WithRegister` item, `WithSetup` or `Start` used to leave the app
  `collage.New` had made open.
- **A `collagetest.Site` can be copied.** Its internal flag no longer trips
  `go vet`'s copylocks check on `s := *site`.

### Documentation

- The plugin registry's `Shutdown` says it does not wait for a plugin still in
  `Init`.

## v0.61.2

One fix in plugin configuration. No exported name changed.

### Fixed

- **A list in plugins-config.json replaces the plugin's default list whole.**
  `PluginConfig` decoded a section's array over the Go defaults' elements, so a
  field an element left out kept the Go element's value: a Go font
  `{A, 700, italic}` and a JSON font `{"family": "B", "file": "b.ttf"}` came back
  as B in bold italic, and a tenant listed in JSON without hosts took the hosts
  of the Go tenant it landed on. The decode also wrote into the caller's slices
  and maps. A top-level list the section has now replaces the default with new
  elements, a top-level map gets the section's entries in a copy, and neither
  touches the caller's memory, on success or on a malformed section.

## v0.61.1

Two test helpers for plugin authors and three fixes. Additions only: no exported
name changed or went.

### Added

- **`collagetest.WithSetup` registers on the application before it starts.** A
  plugin test that needs `app.Handle`, `app.Mount`, `app.Use`,
  `RegisterNotFoundPage` or `RegisterErrorPage` no longer hand-builds its app:
  `WithSetup(func(app *collage.App) error)` runs after `New` and the default
  registrations, before `Start`. Several run in order, and an error fails
  `NewSite` or is returned by `TrySite`.
- **`(*collagetest.Site).Shutdown(ctx)` shuts the site down once.**
  `App.Shutdown` returns its first error on every later call, so a test that shut
  the app down itself, expecting an error, had the same error reported again by
  the cleanup. `site.Shutdown` closes the site and the cleanup does nothing more:
  call it, not `site.App.Shutdown`, when the test shuts down itself.

### Fixed

- **A `Shutdown` before `Start` initialises no plugin.** `Start` used to run every
  plugin's `Init` after the application was already shut down, and nothing was
  left to shut them down. It now fails without initialising any plugin, and
  `ListenAndServe` returns nil, the clean stop that was asked for. Through `App`,
  a `Shutdown` while `Start` is still running waits for `Start` to finish, then
  shuts every plugin down once. The plugin registry itself now also handles a
  `Shutdown` that races `Init`: the plugin in `Init` is shut down as soon as it
  returns and no further plugin is initialised.
- **`collage.ParseOrigin` refuses an empty query or fragment marker.**
  `https://cms.example.com/?`, `https://x/#`, `https://x?` and `https://x#` were
  accepted and returned as they were, so a `Config.BaseURL` of
  `https://x/?` joined into `https://x/?/path`; they are now `ErrInvalidOrigin`.
- **Documentation follows the v1 plugin conventions.** Plugin constructors in the
  docs and README are `x.New(x.Options{})`, and `LoadPluginConfig`'s example uses
  keys the plugins still read.

## v0.61.0

Additions for plugin authors, one stricter rule and one fix. No exported name
changed or went; one behaviour did — a plugin configuration key the plugin does
not know is now an error (see Changed).

### Added

- **`collagetest.NewSite` builds a real application for a plugin's tests.** It
  starts a collage application with the least configuration a test needs — a
  layout with a head hoist and a content slot, a `home` page at `/`, a debug-level
  text logger — and hands back the started `App`, a `Client` on its handler and
  the captured `Logs`. `WithPlugins`, `WithPluginConfig`, `WithTemplates`,
  `WithRegister`, `WithoutHome` and `WithConfig` add what a test needs; plugins'
  `Configure` and `Init` errors fail the test at once, and the application shuts
  down when the test ends. `TrySite` returns the error for a test that expects
  starting to fail. `Logs.Has(level, parts...)` is safe while the site serves
  concurrently. See [plugins](docs/plugins.md#testing-a-plugin).
- **Named route kinds, built file kinds and pipeline stages.** `RouteKindPage`,
  `RouteKindDocument`, `RouteKindAction`, `RouteKindMount`, `RouteKindHandler`
  for `Route.Kind`; `BuiltPage`, `BuiltDocument`, `BuiltAsset` for
  `BuiltFile.Kind`; `StageRoute` … `StagePanic` for `ErrorEvent.Stage`. They are
  untyped string constants equal to the strings the engine already sends, so a
  comparison against the literal keeps compiling; the fields stay `string`, and
  each set may grow.
- **`DocumentBuilder.WithTimeout` gives a document its own handler timeout.**
  `Document.Timeout`, when set, replaces `TemplateConfig.Timeout` for that
  document, on live requests and in static builds alike, so a slow sitemap no
  longer raises the budget of every fragment and feed. A negative timeout is
  `ErrInvalidTimeout`, from the builder and from `Document.Validate`.
- **`collage.Duration` reads a duration from configuration as a string.** A
  `type Duration time.Duration` whose JSON is `"10s"`, `"1h30m"`: a bare number,
  which `time.Duration` reads as nanoseconds (`"banTime": 3600` is 3.6µs), is
  refused with an error naming it, and so is a negative duration; `null` leaves
  the default. Plugins use it for a duration in their options.
- **`collage.IsHTTPS(r)` says whether a request arrived over TLS.** TLS on the
  connection, or `X-Forwarded-Proto: https` — believed only from
  `Server.TrustedProxies` when any are listed, from anyone when none are. It is
  the decision the forgery guard's cookie already made, now one function both
  use; plugins setting `Secure` cookies or HSTS use it instead of reading the
  header.

### Changed

- **`collage.PluginConfig` refuses a key the plugin's options do not have.** A
  misspelt or renamed key — at any depth, or one named after a `json:"-"` field —
  stops the application at `Configure` or `Init` with
  `collage: elagoht/robots: unknown key "dissallow" in plugins-config.json`, where
  it used to be ignored and the option left at its default. Keys still match
  case-insensitively. **Migration:** remove the key the error names, or rename it
  to the one the plugin documents.
- **A plugin configuration value that fails to decode is named by its key.**
  `collage: elagoht/fail2ban: "banTime": write a duration as a string such as
  "10s", not 3600`, where the message used to name the Go field.

### Fixed

- **A start that fails after plugin `Init` shuts the plugins down.** A mount that
  swallows a page, or an error page never registered, used to fail `Start` with
  every plugin left initialised; they are now shut down, once, as a failing `Init`
  rolls back the ones before it. `App.Shutdown` shuts down only plugins whose
  `Init` succeeded and that are not shut down already.
- **`collage.ParseOrigin` refuses an origin with no host.** `https://:8080` was
  accepted; it is now `ErrInvalidOrigin`, from `ParseOrigin` and from
  `Config.BaseURL` validation, which shares it.

## v0.60.1

### Fixed

- **A static build refuses a dangerous `OutDir` before running any of your code.**
  A filesystem root, or a repository root with `Clean`, used to be refused only
  after the application started and every `StaticParams` ran. It is now refused
  first, and checked again, where it then points, just before `OutDir` is
  cleaned or created.
- **A build that stops before rendering reports why in `BuildReport.Errors`.**
  The returned error is still that failure alone; `BuildReport.Errors` now holds
  it at every early stop (start failure, refused `OutDir`, collision, `OutDir`
  not cleaned or created), after the `StaticParams` failures when they had run.
- **A document whose file is a directory another output needs collides up front.**
  A document at `/feed.xml` beside a page or document under `/feed.xml/` stops the
  build with `ErrOutputPathCollision` before `OutDir` is touched, instead of
  failing at write time.
- **An ambiguous `ActionResult` invalidates nothing.** Its `InvalidateTags` used to
  run before the result was refused with a 500.
- **`WithSlot`'s unknown-rule error names the slot and the fragment.**

The rest is tests, comments and documentation: `pkg/collage/errors.go` groups
every sentinel by area, and a regression test pins `ErrInvalidTTL` being reported
once at registration.

## v0.60.0

The public API is sealed for v1.0. `pkg/collage` now exports what an application
or a plugin needs and no more; what only the engine used has left it. Every name
below that changed is listed with what to write instead. Plugins compiled against
v0.59.x need rebuilding; a plugin that uses only `Init(ctx, host)`, `host.Logger()`
and `host.Use(...)` compiles as it was.

### Changed (breaking)

**Building a static site**

- **`NewBuilder(app, opts)` and `Builder.Build(ctx)` are `collage.Build(ctx, app, opts)`.**
  It returns `(*BuildReport, error)`. `ErrNilRenderer` is `ErrNilApp`, and
  `ErrInvalidOutDir` is `ErrEmptyOutDir`.
- **`SkipRecord.Page` and `WarningRecord.Page` are `.Name`.**
- **`ErrDuplicateOutputPath` is `ErrOutputPathCollision`.**

**The `App` and the `Host`**

- **`App` is a defined type with a narrower method set.** Gone from `*App`:
  `Mounts`, `RenderNotFound`, `CSRFMarker`, `PageRouted`, `PrefixDefault`,
  `DefaultLocale`, `BuildFinished`, `HasBuildFinishedHook`, `PluginRedirects`,
  `CaptureResponses`. A build asks the application itself which mounts to copy.
- **`App.Inspect`, `Inspection` and the `Inspected*` types are removed.** The JSON
  `collage inspect` prints is the contract, and is byte-for-byte what it was.
  `CheckCommand` and `InspectCommand` are no longer exported.
- **`collage.Origins` and `host.(collage.Origins)` are `host.OriginFor(ctx, h)` and
  `host.MultiOrigin()`,** methods of `Host` itself. `Origins.Dynamic` is `MultiOrigin`.
- **`Host.RegisterPage` and `Host.FragmentURL` are removed.** `App` keeps both.
- **`collage.Mount` is removed.**
- **Hooks: `PageResolvedHook`/`PageResolvedEvent` and `CacheWriteHook`/`CacheWriteEvent`
  are removed.** Nothing used them. A document now dispatches three hooks:
  `OnDocumentRendered`, `OnCacheInvalidate` and `OnError`.
- **`RouteOf(ctx)` is `RouteInfo(ctx).Kind` and `.Name`.**
- **`CacheInvalidateEvent.Paths` is `Entries`,** each a host and a path.
- **`Cache.Invalidate` is a method of `TaggedCache`,** the interface a store
  implements to index tags itself.

**Pages, fragments and data**

- **Engine-only methods are no longer public:** `Page.Root`, `Guards`, `Guarded`,
  `PathFragments`, `ContentSlotName`, `LayoutFragment`; `Fragment.Bind`,
  `SetDataSource`, `DataSource`, `EffectiveTimeout`, `Slot`, `SlotNames`;
  `Result.HoistEnds`, `Result.FragmentReports`; `RenderContext.Hoisted` and
  `RenderContext.WithFragment` (an application hoists with `Hoist`).
- **`FragmentMetadata` is `FragmentReport`,** with the same fields;
  `Metadata.Fragments` is a `[]FragmentReport`.
- **`JSONOf(status, v)` is `JSON(status, v)`.** The old `JSON(status, []byte)` is
  `JSONBytes(status, body)`.
- **`NoContent(status)` is `Status(status)`.**
- **`WithDependency(tags...)` is `WithDependencyTags(tags...)`,** on page and
  document builders.
- **`DataHandler(fn)` is `LoadTagged(fn)`.**
- **`VariedContext` is gone and `Varied` takes a context:** `Varied(ctx, h)`.
- **`Redirect.StatusCode` is `Redirect.Status`,** and `0` means `302`.
  `Redirect.Permanent` is removed; `WithPermanentRedirect` writes `301`.
- **`Redirect.EffectiveStatus` and `Redirect.Validate` are removed.** A zero
  `Status` still answers 302; a page's or document's `Validate`, and registration,
  check its redirects.
- **`SlotResolverFunc` is `func(ctx, rc)`.** It receives the render's context.
- **`WithSlot(name, required, allowMultiple bool)` is `WithSlot(name, rules ...SlotRule)`,**
  with `SlotRequired` and `SlotSingle`.
- **`ErrMissingTTL` is `ErrInvalidTTL`.**

**Configuration**

- **`Template.DevMode` is removed;** `Config.DevMode` is the one switch.
- **`Locale.DisablePathLocale` is removed.**
- **`Cache.MaxKeysPerTag` is removed;** `MaxEntries` caps the keys tracked under
  a tag too, and a negative `MaxEntries` keeps that cap at 10000.
- **`Security.NoSniff *bool` is `Security.DisableNoSniff bool`.**
- **`Cache.Type` is a `CacheType`.** An untyped literal (`Type: "disk"`) still
  compiles; a string variable needs converting: `Type: collage.CacheType(v)`.
- **`Config.Validate`, `ApplyDefaults` and `IsDevMode` are no longer public.**
  `WithCacheControl` and `WithoutBuildCopy` are functions now; call sites do not
  change.

**Errors.** Sentinels an application could not meaningfully match are gone:
`ErrNilConfig`, `ErrNilPage`, `ErrNilPlugin`, `ErrEmptyCommandName`,
`ErrEmptyPluginName`, `ErrNoRootFragment`, `ErrEmptyCacheVersion`,
`ErrDictKeyNotString`, `ErrDictOddArgs`, `ErrNoMountForAsset`,
`ErrEmptyLocaleDefault` and `ErrEmptyTemplateRoot`. Renamed to the sentinel that
stays: `ErrUnsupportedCache` is `ErrInvalidCacheType`, `ErrInvalidHandlerPrefix`
is `ErrInvalidPrefix`, `ErrInvalidBaseURL` is `ErrInvalidOrigin`, `ErrNoMethods`
is `ErrNoActionMethods`.

### Changed (behaviour)

- **`Build()` returns an independent copy.** Calling it twice gives two values, and
  `WithPath` on the builder afterwards no longer reaches the one already built or
  registered. Two values are two pages to the application, so code that called
  `Build()` again where it meant the same page now fails:
  `app.RegisterNotFoundPage(nb.Build())` with `WithNotFoundPage(nb.Build())`
  fails `Start` with `ErrUnregisteredErrorPage`, and
  `return collage.RenderPage(pb.Build()), nil` in an action handler answers 500 with
  `ErrUnregisteredPage`. Build once and keep the `*Page`.
- **`WithPath` refuses a repeated or an empty locale,** recording
  `ErrDuplicateLocalePath` or `ErrInvalidLocale`. The first path is kept. A
  document's `WithPath("", p)` used to mean `AtRoot(p)` and is now refused; write
  `AtRoot(p)`. `AtRoot` called twice records `ErrDuplicateLocalePath` the same way.
- **An `ActionResult` with two or more of `Location`, `Fragment`, `Page` and `Body`
  is a 500** with `ErrAmbiguousActionResult`. A result with only `Status` or only
  `Header` answers as before.
- **A document whose output file collides with another document's or a page's stops
  the build** with `ErrOutputPathCollision`, `index.html` of a page included. The
  build stops before `OutDir` is touched: nothing is cleaned or written, no 404
  page or mounted file is copied, and `BuildFinishedHook` is not called.
- **Documents' `StaticParams` run before pages render** in a static build.
- **An action built with errors is refused at registration.**

### Added

- **`collage.Build`,** with `BuildOptions`, `BuildReport`, `SkipRecord` and
  `WarningRecord` defined in `pkg/collage`.
- **`Host.OriginFor` and `Host.MultiOrigin`,** also on `ConfigHost`.
- **`SlotRule`, `SlotRequired`, `SlotSingle`.**
- **`CacheType`, `CacheMemory`, `CacheDisk`.**
- **`ErrDuplicateLocalePath`, `ErrInvalidLocale`, `ErrAmbiguousActionResult`.**
- **`AfterRenderEvent.Path`.**
- **`ActionBuilder.BuildErr`.**
- **An `api` CI job** reports the changes to `pkg/collage` against the last final
  release, and from v1.0 gates them.
- Public doc comments in `pkg/collage` name no internal identifier.

## v0.59.0

### Added

- **`collage plugin add` and `del` take several plugins.** Each is added or
  removed in a step of its own: one that fails is put back, the others stay,
  and the command exits 1. A name that cannot be read refuses the whole command
  before anything changes. With several, a summary follows, and `add` prints the
  setup notes together at the end. `del` removes a plugin another one on the
  list requires after that one.
- **Bundles, written `@name`.** `@prod` (health, secure, compress, minimizer,
  accesslog, ratelimit), `@site` (robots, sitemap, meta, jsonld, favicon),
  `@blog` (`@site` with markdown, feed, toc, highlight, ogimage), `@forms`
  (session, flash, validate, honeypot), `@live` (live, websocket) and `@ops`
  (prometheus, errortrack). `collage plugin add @prod`; `del @name` passes over
  a bundle's plugins the project does not have.
- **`collage new <name> --with @blog,@prod`.** It takes bundles and plugins,
  comma-separated or in more than one `--with`, checks them before writing
  anything, and adds them once the project is scaffolded.
- **The scaffolded `main.go` reads `BASE_URL` into `Config.BaseURL`.**

### Changed

- **`collage plugin del x@v1` is a usage error (exit 2),** refused before any
  plugin on the list is removed; it was exit 1.

## v0.58.0

### Added

- **`collage plugin add <owner/name>`.** It fetches `github.com/<owner>/collage-<name>`
  with `go get`, checks that it is a collage plugin, adds it to `plugins.go`
  (with its import, in the place its manifest's `order` asks for), and adds an
  empty section `"<owner/name>": {}` to `plugins-config.json`. A full module
  path (`gitlab.com/x/y`) is used as written, and `@version` pins a release.
  Plugins it `requires` are added first. Any failure leaves `go.mod`, `go.sum`,
  `plugins.go` and `plugins-config.json` as they were.
- **`collage plugin del <owner/name>`.** It removes the plugin from `plugins.go`
  and `plugins-config.json`, prints the settings its section held, and runs
  `go mod tidy`. It refuses while another plugin requires it, and when it
  cannot read another plugin's `collage.json` to tell.
- **`collage plugin check [-release] [-run] [dir]`.** It checks a plugin
  repository against the contract `add` enforces:
  - a `collage.json` naming it `owner/name`;
  - a `New` that can be called with nothing configured;
  - with `-release`, a release tag;
  - with `-run`, that `New`, called as `add` writes it, runs and returns a
    plugin whose `Name()` is the manifest's name. It does not configure or
    start the plugin.
- **The manifest gains `requires`, `order` and `setup`.**
- **`collage.FindPlugin[T](host)`.** It finds another of the application's
  plugins by type, from `Configure` or `Init`.
- **New projects get `plugins.go`,** and `main.go` passes `Plugins: plugins()`.
  An existing project gets the file from its first `collage plugin add`. Pass
  `plugins()` to `collage.New` once, as the command says.

## v0.57.0

### Added

- **`WithStreamingBody()`.** An action marked with it reaches its handler with
  the body unread: nothing before the handler parses a form out of it, so the
  handler reads it as a stream. Its forgery token is accepted only in the
  `X-CSRF-Token` header (`Security.CSRFHeaderName`), never in a form field; a
  request without it is refused with `403` and the new
  `ErrCSRFHeaderRequired`, which wraps `ErrCSRFMissing` and names the header.
  A plain HTML form therefore cannot post to such an action — a `fetch()` with
  the header can. `MaxBodyBytes` bounds it as before, and its 4 MiB default
  must be raised for a large upload.
- **`ErrStreamingBody`.** `BeforeActionEvent.Form()` returns it for a
  `WithStreamingBody()` action without reading the body, so a plugin cannot
  consume the stream the handler reads. A plugin that inspects forms treats it
  as "this action has no form to check".
- **`ErrStreamingBodyMethod`.** Registration refuses `WithStreamingBody()` on
  an action answering none of `POST`, `PUT` or `PATCH`, naming the action and
  its methods.
- **`WithBodyTimeout(d)`.** A request to the action gets `d` to be read and
  answered, in place of the server's `ReadTimeout` and `WriteTimeout`: both
  deadlines are set to now plus `d` through `http.ResponseController`, after the
  page's guards — and, for a streaming action, after its forgery check, which
  reads only a header — and before anything reads the body. The defaults (15s, 30s)
  otherwise end any upload slower than that, and `WriteTimeout`, which net/http
  starts when the headers arrive, covers the upload too — raising only
  `ReadTimeout` lets the handler store a file whose answer the client never
  receives. A negative `d` is refused at registration with the new
  `ErrNegativeBodyTimeout`. A `ResponseWriter` wrapper without `Unwrap` cannot
  take the deadline; the request then runs under the server's, with a warning
  logged once. See "Uploads" in docs/deployment.md, which also covers nginx,
  Caddy, CDN body limits and `elagoht/health`'s `maxInFlight`.

### Changed

- **A body that hit its bound answers `413`, whatever error the handler returned.**
  A handler that read past `MaxBodyBytes` and failed was a `413` only when its
  error wrapped the read's `*http.MaxBytesError`; one that replaced it with its
  own ("upload failed") was a `500`. Now the action checks whether its body ran
  into the bound, for every action, and answers `413` with the limit. The
  handler's own error is kept in what is logged and handed to error hooks.
  `errors.As` finds a `*http.MaxBytesError` on the error the hooks receive.
  It is logged at warn, not error.
- **An action whose body was cut off answers `400`, or `408`.** When a read of
  the body fails because the client went away, the connection broke, or the read
  deadline passed, and the handler then fails, the action answers `400` — `408`
  for the deadline — instead of `500`. A client that left never receives it,
  and under `WithBodyTimeout` neither does one that ran out of time, since the
  write deadline passed with the read one: the status is then for metrics and
  access logs. It is logged at debug and handed to no error hook, as a request
  whose context was cancelled already was: every cancelled upload was an
  error-level line and an error-tracker event, and anyone could make them as
  fast as they liked. This holds for every action,
  bounded or not; a read after `Close` is still the handler's bug, and a `500`.
- **`docs/deployment.md`'s timeout example no longer raises `ReadTimeout` alone**
  for uploads, which led into the trap described under `WithBodyTimeout`.

### Upgrading

- **elagoht/honeypot must be upgraded with this release** if any form it
  protects posts to a `WithStreamingBody()` action: its released versions refuse
  any request whose `BeforeActionEvent.Form()` fails, and for such an action it
  always does, with `ErrStreamingBody`. Its streaming-aware release lets the
  request through. It cannot protect a streaming action either way, since
  nothing reads that action's fields.

## v0.56.0

### Breaking

- **An action may not declare `OPTIONS`, `TRACE` or `CONNECT`.** Registration
  fails with the new `ErrInvalidActionMethod`, naming the action and the
  method, however the action was built: `NewAction(…).WithMethods`, a page's
  `WithAction` or `WithActionFor`. The router answers `OPTIONS` itself with the
  path's `Allow` list, and the forgery check counts `OPTIONS` as a safe method,
  so an action claiming it would change state with no token checked. A CORS
  preflight belongs in a middleware, ahead of the router.

### Added

- **`COLLAGE_DEV_HOST`.** A comma-separated list of extra names a development
  server answers — a docker-compose service name, an `/etc/hosts` name,
  several tenant hosts, a LAN name while bound to `0.0.0.0`. An application
  in development mode reads it from its process environment; `collage dev`
  reads it from the shell or the environment file, and starts the program
  with the list plus its own `HOST`.
- **`ServerConfig.ReadHeaderTimeout`.** A deadline for the request headers
  alone, so a client sending them a byte at a time is dropped early while a
  large body still gets the whole `ReadTimeout`. Zero, the default, uses
  `ReadTimeout`, which is what the server has always done; a negative value is
  `ErrNegativeDuration`. `collage dev`'s proxy keeps its own fixed 10s.

### Security

- **`X-Forwarded-Proto` is believed only from `Server.TrustedProxies` when it
  is set.** The header decides whether the forgery cookie is `Secure`, and a
  client reaching the server directly could send it too. With
  `TrustedProxies` set, it counts only when `RemoteAddr` is a listed proxy,
  matched as `ClientIP` matches. Empty, it is believed from anyone, as before.
- **Development mode refuses a `Host` that does not name this machine.** Run
  directly with `COLLAGE_DEV=1`, an application had no Host check, so a page on
  another site that made its own name resolve to it (DNS rebinding) could read
  the development error pages, the reload stream and a development toolbar. In
  `DevMode` the handler now answers 403 unless the `Host` is localhost or a
  name under `.localhost`, an IP address, a reserved name (`example.com`,
  `example.net`, `example.org` and names under them; names under `.example`,
  `.test`, `.invalid` — nobody can register one to rebind with, and httptest's
  requests use `example.com`), `Server.Host`, or a name in `COLLAGE_DEV_HOST`.
  `collage dev`'s proxy applies the same rule, now shared, reserved names
  included. The 403 quotes the refused Host and names the setting. A capture
  (`CaptureResponses`, which a development build runs with the production
  `BaseURL`'s host) is not checked. **Upgrading:** a `DevMode` test or request
  that uses a custom Host outside the reserved names — a tenant domain, say —
  now gets 403; set it as `Server.Host`, list it in `COLLAGE_DEV_HOST`, or
  use a reserved name such as `tenant.test`.
- **`ListenAndServe` warns when development mode is reachable from other
  machines.** Bound to anything but loopback — `0.0.0.0`, `::`, a LAN
  address — with `DevMode` on, it logs one Warn that the
  development error pages and tooling expose the application's internals.
  `collage dev` logs the same Warn when its own proxy is bound that way: the
  program behind it binds loopback and would never say so.

### Docs

- **The query string's place in the cache key is deliberate.** The caching
  guide now says why the full raw query is the default and stays so — changing
  it would silently serve one page for another — that `WithCacheParams` is the
  opt-in allowlist, and that `MaxEntries`, `MaxBytes` and the tracker's caps are
  what bound the memory a client minting variants can cost.

## v0.55.0

### Added

- **`ServeHook`.** A plugin implementing `OnServe(ctx)` — a scheduler, a queue
  worker — is called once, in registration order, when `ListenAndServe` has
  bound its port and before it serves. Its ctx is cancelled when the drain
  starts, at the same moment `OnDrain` runs, whether the stop came from a signal
  or from `App.Shutdown`. `Start()`, `Handler()`, a static build, a plugin
  command and a `Shutdown` before `ListenAndServe` never call it; an application
  serving `Handler()` from its own `http.Server` starts such plugins itself. A
  panic in `OnServe` is contained and logged at Warn with the plugin's name, and
  the server still serves. `OnServe` must not block.

### Changed

- **The scaffolded `main.go` shuts the application down after a static build
  or a plugin command.** Neither serves, so until now no plugin's `Shutdown`
  ran there: what a plugin held was never flushed, and work handed to a queue
  plugin such as elagoht/jobs during a build vanished without being logged.
  `main.go` now calls `app.Shutdown` once the build or the command is done, and
  only after a successful start, since a failed one has already shut down what
  it started. `DispatchCommands` and `Builder.Build` themselves still do not
  shut down: a caller may go on to serve the same App. Existing projects add
  the call by hand; see `shutdown` in a newly scaffolded `main.go`.

### Docs

- **An application serving `app.Handler()` from its own `http.Server` calls
  `app.Shutdown(ctx)` after its server has stopped.** That has always been
  what runs the plugins' `Shutdown`, but nothing said so; the deployment guide
  now does, and its example calls `app.Start()` first so that an Init failure
  is returned rather than only logged. The stop-time rule there counts the one
  second elagoht/jobs may wait past its deadline.

## v0.54.0

### Fixed

- **A static build exports a nonce page with its strategy's `Cache-Control`.**
  A `PersonaliseHook` that set `Personal` — a CSP nonce, as elagoht/secure's —
  made the build's header capture answer `private, no-store`, so the exported
  file, which carries no nonce, was exported as one no host may cache. The
  capture still runs the hook, but sets its `Personal` aside: the page gets the
  `Cache-Control` its strategy gives (`public, max-age=…` for an incremental
  page), and no `capture-personal` warning. A reader's request is unchanged,
  still `private, no-store` with no `304`.
- **The demo scaffold says what to do with `/healthz` when elagoht/health is
  added.** `documents/health.go`, its test and the README now say to delete the
  document or move the plugin's `livePath`: the plugin's middleware answers
  `/healthz` first, so the document would never be reached.

### Changed

- **`capture-personal` is narrower.** It is still raised for a page answered
  `private` or `no-store` beside a header that differs between answers, but a
  `PersonaliseHook` can no longer cause it, and its message says so. A
  development build no longer raises it either: its `no-store` is what
  `capture-dev-mode` already warns about.

## v0.53.0

### Added

- **`ServerConfig.DrainDelay`: a drain phase before shutdown.** On a shutdown,
  whether from `SIGINT`/`SIGTERM` or `App.Shutdown`, the server first turns
  keep-alives off and goes on serving for `DrainDelay`, so a load balancer can
  see readiness fail and stop sending traffic before the port closes. Only then
  do the streams close, the server stop and the plugins shut down, as before.
  `ShutdownTimeout` starts after the drain, so a stop can take
  `DrainDelay + ShutdownTimeout`. A second signal, or a done `Shutdown` ctx,
  ends the wait early; development mode ignores it. A negative value fails
  `Validate` with `ErrNegativeDuration`. When you call `App.Shutdown(ctx)`
  yourself, that ctx bounds the drain and the in-flight requests together, and
  a ctx that is done ends a drain already under way.
- **`DrainHook`.** A plugin implementing `OnDrain()` is told once, when the drain
  starts, even with no delay; a panic in one is contained and logged at Warn
  with the plugin's name. A `Shutdown` before `ListenAndServe` calls `OnDrain`
  too, without waiting, since nothing is serving — on plugins whose `Init` never
  ran.

Nothing changes by default: `DrainDelay` is 0, which does not wait. The signal
path now logs `collage: draining` before `collage: shutting down`.

## v0.52.0

### Breaking

- **A redirect with a control character in its `From` or `To` fails
  registration with `ErrInvalidRedirect`.** `\r`, `\n`, any other control
  character and the Unicode line and paragraph separators are refused by
  `WithRedirect` and `WithPermanentRedirect` on pages and documents, and by
  `Redirect.Validate` for a redirect built as a struct literal. Such a rule
  could only ever break the redirect file a static host reads, or the
  `Location` header a server sends.
- **A static build now sends requests to the application's own handler: two
  per written file, three when the first answer is the trailing-slash
  redirect,** whenever a plugin implementing `BuildFinishedHook` is
  registered to read what they answer; without one, none are sent. They are
  asked over HTTPS (a TLS connection state and the `https` scheme) when
  `Config.BaseURL` is `https`, so a header sent only over HTTPS, such as
  collage-secure's `Strict-Transport-Security`, is captured. They are in-process — no network — and middleware and the
  request hooks see them, so a plugin that counts or limits traffic
  (analytics, a rate limiter, a ban list) now sees a build's paths. It should
  skip a request for which `collage.IsCapture(r.Context())` is true. Middleware
  that sets response headers must not skip it: what it sets is what the
  deployed file is served with. A capture request never writes the response
  cache and never shares a reader's render, but it renders: `BeforeRender`,
  `AfterRender` and `DocumentRendered` run for it as for any request, so a
  plugin that writes files or keeps a tally from those hooks sees each built
  path two or three more times, and should check `collage.IsCapture(ctx)`
  too.
- **Two redirects the router takes for one now fail a static build with
  `ErrDuplicateRedirect`** — `/old` and `/old/`, `/blog/{slug}` and
  `/blog/{x}`, from any source: a page, a document or a `RedirectSource`
  plugin. On a static host the host's own precedence would pick one. Remove
  one of the two.
- **A redirect matching the path of a file the build wrote now fails it with
  `ErrRedirectShadowsFile`** — `/about` or `/about/` beside
  `about/index.html`, `/docs/{rest...}` beside `docs/x/index.html`. Narrow the
  redirect's pattern so it no longer matches the file, or drop the page.

### Added

- **A static build records each file's response headers.** After every file is
  written, and when a plugin implementing `BuildFinishedHook` is registered to
  read them, the build asks the handler for each one's path twice and puts the
  answer on the file: `BuiltFile.Status` and `BuiltFile.Headers`. Request-specific
  headers are left out (`Date`, `ETag`, `Last-Modified`, `Content-Length`,
  `Set-Cookie`, `Vary`, `Content-Encoding`, `Transfer-Encoding`, `Connection`,
  `Age`, `X-Collage-Render-Time`), and so is a header whose value differs
  between the two answers, such as a CSP nonce: a static file cannot carry it.
  The 404 pages and the root redirect, which the build makes itself, are not
  asked for. `BuiltFile.Captured` is true for every file the build asked for —
  also when that capture failed or was never reached because the capture
  stopped, which leaves `Status` 0 — and false for the files the build made
  itself. A build whose context ends is not handed to `BuildFinishedHook`.
  What the capture found is reported as warnings, never as a failed build:
  - `unstable-header` — a header left out because it differs between two
    answers, once per header name with a count of paths;
  - `capture-status` — a file answered with a non-2xx status, or with two
    different statuses;
  - `capture-failed` — a path not answered (each request has a deadline), or
    the capture not run at all;
  - `capture-dev-mode` — a build in development mode, whose headers (such as
    `Cache-Control: no-store`) are not the ones to deploy;
  - `capture-personal` — pages answered with `Cache-Control` `private` or
    `no-store` beside a header that differs between answers (a CSP nonce):
    that header is left out, so the exported file is no longer personal, and
    the `Cache-Control` only keeps a host from caching it. One warning names
    how many and the first few.
- **`collage.IsCapture(ctx)`** reports whether a request is the build's
  capture.
- **`BuildFinishedEvent.Redirects`** hands `BuildFinishedHook` every redirect
  the site declares, as `collage.BuiltRedirect` (`From`, `To`, `Status`,
  `Source`): every page's in registration order (`Source` `"page:<name>"`),
  every document's (`"document:<name>"`), then every plugin's. `Status` is the
  redirect's `EffectiveStatus()`.
  A page registered only with `RegisterNotFoundPage` or `RegisterErrorPage`
  is never matched, so its redirects are left out.
- **`collage.RedirectSource`**: a plugin with `Redirects() []BuiltRedirect`
  has its rules carried by a static export, and each is checked as a
  registered redirect is: `From` starts with one `/` and parses as a route
  pattern, and every placeholder in `To` is captured by it. `To` may also be
  an absolute `http` or `https` URL; a 410 (a path that is gone) has no `To`,
  and 301, 302, 307 and 308 need one. Any other `Status` fails the build with
  `ErrInvalidRedirectStatus`, and a malformed rule, or a control character in
  one, with `ErrInvalidRedirect`, naming the rule and the plugin.

### Fixed

- **A static build no longer tries to render a page registered only with
  `RegisterNotFoundPage` or `RegisterErrorPage` at the path it was given**,
  which failed the build: such a page is never matched.

## v0.51.2

### Fixed

- **`collage inspect`'s type table no longer lets two packages of one name
  overwrite each other.** `a/models.User` and `b/models.User` both key as
  `models.User`; the second replaced the first, and an editor then flagged the
  first type's fields. Such a key is now `{"kind": "struct", "ambiguous": true}`
  with no fields or methods (`kind` is empty if the types differ in kind), for
  an editor to treat as unknown, whatever order the types were reached in. A
  fragment's `dataType` is unchanged, and `version` is still `1`.

## v0.51.1

### Added

- **`collage inspect`'s type table lists embedded fields and container element
  and key types, for editors.** An exported embedded field, which a template
  reaches as `{{.Base}}`, is now listed among the fields with
  `"embedded": true`; the fields it promotes are listed as before. A named
  pointer, slice, array, map or chan (`type Posts []Post`) now carries
  `"elem"`, the type it holds, and a map also `"key"`. Both are additions: the
  output's `version` is still `1`.

## v0.51.0

### Added

- **The template type checker judges argument types.** Up to v0.50.x only the
  number of a call's arguments was checked. An argument is now judged against
  its parameter as `text/template`'s `evalArg` judges it: a constant by the
  parameter's kind (`{{upper 1}}`, `{{printf 1}}`, `{{uint -1}}`, `{{i8 1.5}}`,
  `nil` where nil cannot go), any other value of a known type by
  assignability, one pointer deref, or the address of an addressable value
  (`{{upper .Count}}`, `{{edit .Owner}}` for a `func(*User)` on data passed by
  value), for template functions, methods, variadic parameters and the value
  piped in alike. `call` checks what it passes as `text/template`'s `call`
  does, and `index` checks a map key's type and that a slice, array or string
  is indexed by an integer.
- **`slice` of the wrong kind is reported**: of anything but a string, a slice
  or an array, of a string by three indexes, by more than three indexes, by a
  non-integer, or of an array `text/template` cannot address.

An application may now fail at startup over a call in a template that would
already have failed when it rendered: the check reports it earlier, and still
reports nothing `text/template` does not fail on.

## v0.50.2

### Fixed

- **The template type checker knows the type of a number constant.** A number
  was unknown to it, so nothing read from one was reported:
  `{{$n := 1}}{{$n.Nope}}`, `{{(1.5).Nope}}`, `{{len 1}}`, `{{index (1) 0}}`
  and `{{range $i, $v := 2}}` passed `RegisterPage` and failed when they
  rendered. A number is now typed as `text/template` types it — `int`,
  `float64` for one written with a point or an exponent, `complex128` — and one
  that overflows `int`, or `nil` used as a command, is reported too. A number
  given to a function's typed parameter is still not judged.

## v0.50.1

### Fixed

- **A literal condition that declares a variable no longer reports its dead
  body.** The template type checker skips a branch a literal condition never
  takes, but not when the condition declared or assigned a variable, so
  `{{if $x := false}}{{.Nope}}{{end}}`, `{{if $x := 0}}…`, `{{with $x := ""}}…`
  and `{{if $x = false}}…` were reported although html/template renders them
  fine, and `RegisterPage` failed with `ErrTemplateType`. A declaration holds
  the pipeline's value, so the literal now decides the branch either way.

## v0.50.0

### Breaking

- **Values shared in a render are read and written through typed keys.** A
  `collage.Key[T]` is a name and a type, declared once at package level; the
  value `Set` stores under it is the one `Get` reads, as `T`, and a write of
  the wrong type does not compile. `Once` and `Cached` take keys too, and a
  plugin's `OnAfterRender` reads a render's values with the key's `In`. Plugin
  configuration is read with `collage.PluginConfig`, and `Page.SEO` is gone:

  | Before | After |
  | --- | --- |
  | `rc.Set("k", v)` / `collage.Get[T](rc, "k")` | `k := collage.NewKey[T]("k")`, then `k.Set(rc, v)` / `k.Get(rc)` |
  | `collage.Once(rc, "a:"+id, fetch)` | `collage.Once(rc, aKey.With(id), fetch)` |
  | `collage.Cached(rc, "a:"+id, ttl, tags, fetch)` | `collage.Cached(rc, aKey.With(id), ttl, tags, fetch)` |
  | `ev.Data[k]` in `OnAfterRender` | `key.In(ev.Values)`, with the key the plugin or application that set it exports |
  | `WithSEO(k, v)` | delete; head content goes through `HoistTitle` or `{{hoist}}` |
  | `host.Config(&cfg)` | `cfg, err := collage.PluginConfig(host, defaults)` |

  A key is its name *and* its type: `NewKey[A]("x")` and `NewKey[B]("x")` are
  two keys and hold two values. A value can only be read as the type it was
  stored as, so the runtime type-mismatch errors are gone: reading `"x"` as `B`
  after storing an `A` finds nothing, not the wrong type. `With(part)` derives
  `name:part` with the same type. The values `Set` stores and the ones `Once`
  fetches stay apart, as before. `NewKey` panics on an empty name, and using a
  zero `Key` that never went through `NewKey` panics too.
  See [docs/fragments.md](docs/fragments.md#how-a-pages-fragments-run).
- **`collage.PluginConfig(host, defaults)` replaces `host.Config(&cfg)`.** It
  returns the plugin's section decoded over a copy of `defaults`; no section, or
  an empty or `null` one, returns `defaults` unchanged. A malformed section
  returns `defaults` with an error naming the plugin
  (`collage: plugin "x" configuration: …`). A map or slice inside `defaults` is
  still decoded into in place. See
  [docs/plugins.md](docs/plugins.md#configuration).

### Added

- `collage.Key[T]`, `collage.NewKey[T](name)`, and its `With`, `Name`, `Get`,
  `Set` and `In`.
- `collage.RenderValues` and `AfterRenderEvent.Values`: a finished render's
  values, read with `Key.In`.
- `collage.PluginConfig` and `collage.ConfigReader`, which `Host` and
  `ConfigHost` satisfy and only collage's hosts can.

### Removed

- `collage.Get[T]`, `RenderContext.Get`, `RenderContext.Set` and
  `RenderContext.SharedData`.
- `collage.ErrOnceTypeMismatch` and `collage.ErrCachedTypeMismatch`: a key of
  one type cannot reach a value of another.
- `Page.SEO` and `PageBuilder.WithSEO`: nothing read them.
- `Host.Config` and `ConfigHost.Config`.
- `AfterRenderEvent.Data`.

## v0.49.0

### Breaking

- **A fragment's data is a `collage.Data`, not `any`.** `WithData` now takes a
  sealed `collage.Data`, made only by `collage.Load`, `collage.DataHandler`,
  `collage.Value` and `collage.Effect`, and `WithDataHandler` is gone. Every
  fragment that has data changes one call:

  | Before | After |
  | --- | --- |
  | `WithDataHandler(collage.Load(fn))` | `WithData(collage.Load(fn))` |
  | `WithDataHandler(collage.DataHandler(fn))` | `WithData(collage.DataHandler(fn))` |
  | `WithDataHandler(collage.Effect(fn))` | `WithData(collage.Effect(fn))` |
  | `WithDataHandler(fn)`, `fn` returning `(any, []string, error)` | `WithData(collage.DataHandler(fn))`, with `fn` returning its real type |
  | `WithData(v)` | `WithData(collage.Value(v))` |
  | a factory returning `collage.DataHandlerFunc` | returns `collage.Data` |

  `collage.DataHandlerFunc` is gone with it, and so are `Fragment`'s exported
  `DataHandler` and `Data` fields: a plugin that read them, or built a
  `Fragment` with them, sets its data through the builder. The template's data
  type comes from the function's signature or the value, so a handler and the
  type its template is checked against cannot disagree. `WithData(nil)`, like a
  constructor handed a nil function, is no data. Tags, the data dropped on an
  error, timeouts, prefetching, `Static()` and `Shared()` behave as before. See
  [docs/fragments.md](docs/fragments.md#data-handlers).
- **Setting a fragment's data twice is now an error.** Before, two `WithData`
  calls, or two `WithDataHandler` calls, silently kept the last one; only fixed
  data together with a handler was `collage.ErrConflictingData`. Now any second
  `WithData` on a builder records `collage.ErrConflictingData`
  (`fragment "x" has its data set twice`), which `RegisterPage` returns: keep
  the one call you mean and drop the other.
- **An application may now fail at startup over a template that would already
  fail when rendered.** `RegisterPage` checks each fragment's template against its
  data's Go type and refuses the page when an expression is certain to fail —
  `{{.Titel}}` on a type with no such field, a pointer-receiver method on data
  passed by value, a `range` over a string, the wrong number of arguments to a
  method. Each such template already failed every render that reached it; it now
  fails before the first request instead. The error names the page, the
  fragment, the file, the line and the column, and suggests the closest name:

  ```
  collage: page "post": fragment "post-body" (post.html:1:6): {{.Titel}}: type blog.Post has no field or method Titel (did you mean Title?)
  ```

  Every finding in the page's own fragments arrives at once, joined; its
  not-found page and then its error page are checked only once the page itself
  passes, so their findings come once the page's own are fixed. `WithoutTypeCheck()` on a
  fragment leaves it out of the check while it is fixed. Run against real
  applications before this release, the check reported nothing that would have
  rendered.

### Added

- **Template type checking at registration.** Every fragment a page reaches —
  layouts, content, slot fills, fallbacks, inline fragments, fragment paths and
  the `NotFoundPage` and `ErrorPage` — is walked against its data's type, partials
  once per type they are handed. Reported: fields and methods the type does not
  have or does not export, pointer-receiver methods on values that are not
  addressable, `.key` on a map keyed by a named string type, wrong argument counts
  and result shapes of methods, functions and built-ins, `range`, `len` and
  `index` on the wrong kind, `call` of a non-function. Not reported: anything read
  from an interface or a `map[string]any`, nil data, nil pointers inside data,
  and argument types. A fragment with no data, or with `collage.Effect`, is
  checked for its function calls only. Fragments a slot resolver returns at
  render time are not checked. See
  [docs/fragments.md](docs/fragments.md#how-templates-are-checked).
- **`collage.TemplateTypeError` and `collage.ErrTemplateType`.** Each finding is a
  `*TemplateTypeError` — `Page`, `Fragment`, `Template`, `Line`, `Col`, `Expr`,
  `Reason`, `Suggestion` — and matches `ErrTemplateType` with `errors.Is`.
  `Line` and `Col` are where `text/template` would name the error, which may lie
  inside `Expr`.
- **`FragmentBuilder.WithoutTypeCheck()`**, leaving one fragment's template out of
  the check. A handler declared to return an interface — `collage.Load[any]` —
  leaves its type unknown, and its template is not walked either.
- **`collage.Value(v)`**, data fixed when the program starts, replacing
  `WithData(v)`. When `v`'s static type is an interface, the template is checked
  against the type it holds; a nil interface value is no data.
- **`collage inspect` describes fragment data.** Each fragment gains `dataType`
  (the Go type, `"nil"` for none, `null` when unknown) and, when built
  `WithoutTypeCheck()`, `"typeCheck": false`; the root gains `types`, the
  exported fields and methods of every named type the data reaches, standard
  library types left opaque. The format's `version` stays `1`. See
  [docs/cli.md](docs/cli.md#collage-inspect).

## v0.48.0

### Changed

- **A nested render copies its markup less.** Each fragment's template used to
  execute into a buffer of its own, which was then copied into the fragment's
  buffer and again into its slot's, every one of them grown from nothing. A
  fragment now executes straight into its slot's buffer, the slot buffers are
  reused across renders, and a template that fails partway is cut back out of the
  shared buffer, so a failed fragment still leaves nothing of itself behind. The
  saving follows the markup: a page of a layout, a content fragment and three
  50-row listings (about 14 KB) went from 312 KiB to 185 KiB per render and 8%
  less time, against 136 KiB for the same markup from `html/template` alone;
  `BenchmarkPage_DynamicNested` measures both. Pages of a few small fragments
  allocate 1–5% less and take the same time.

### Docs

- **GOMEMLIMIT beside a memory cache.** `MaxBytes` bounds what the cache holds, not
  the process: the collector lets the heap reach about twice what is live, so a
  full 256 MiB cache measured about 720 MB of RSS. See
  [docs/deployment.md](docs/deployment.md).

## v0.47.0

### Added

- **ServerConfig.TrustedProxies and collage.ClientIP.** `ClientIP(r)` is the
  address of the client a request comes from, as a `netip.Addr`. With no
  `TrustedProxies`, the default, it is `RemoteAddr`'s host and `X-Forwarded-For` is
  never read, so a client cannot forge it. `TrustedProxies` lists the addresses and
  CIDR ranges of the proxies in front of the server (`"10.0.0.0/8"`, `"127.0.0.1"`);
  a request from one of them names its client in `X-Forwarded-For`, read from the
  right: the first address that is not a trusted proxy is the client, or the
  leftmost when every one is. A header entry may carry a port (`9.9.9.9:4567`,
  `[2001:db8::1]:443`) or brackets (`[2001:db8::1]`), as some proxies write it. An
  entry that is still not an address (`unknown`) met before any untrusted address
  leaves the client unknown: `ClientIP` is then the zero `netip.Addr`, never the
  proxy, which would make every visitor one client. List every hop between the
  client and the server, a CDN's published ranges included. An entry that is
  neither an address nor a range makes `collage.New` fail, naming it; one with zero
  bits (`0.0.0.0/0`, `::/0`) is accepted, but `collage.New` logs a Warn: it lets any
  client name any address. The address is unmapped and has no zone, and is the
  zero `netip.Addr` when `RemoteAddr` holds none — as in a shared page render. A
  plugin keying anything on the client, a rate limit or a ban, should use it rather
  than `RemoteAddr`. See [docs/deployment.md](docs/deployment.md) and
  [docs/plugins.md](docs/plugins.md).

### Fixed

- **RequestHook sees requests answered before routing.** A path with an encoded
  slash (`/a%2f..%2fb`, answered 404) and a dirty path (`/a/../b`, redirected with
  301, or 308 for a method other than GET and HEAD) were answered before the
  request hooks ran, so a plugin observing traffic through `RequestHook` — the
  tracing plugin elagoht/otel among them — never saw them, and they are a
  scanner's probes more often than not. The hooks now run first, after the
  trusted proxies are known, so `ClientIP` works inside `OnRequest`; their finish
  is called with the status written — once, unless serving the request panics. For such a request `RouteOf` is empty
  and `r.URL` is the raw path. `Metrics.HTTPResponse` and collage's request span
  still skip it, so a metrics plugin reading `HTTPResponse` (elagoht/prometheus) and
  one sitting in middleware (elagoht/accesslog) still do not see it.

## v0.46.0

### Fixed

- **`collage dev` no longer fills the Go build cache with copies of `static/` and `templates/`.**
  A scaffolded `main.go` embedded both, and every rebuild — every save, since a
  change anywhere recompiles `main` — stored another copy of everything it embedded
  in the Go build cache, kept there for five days: a 30 MB `static/` grew the cache
  by about 30 MB per save. `collage dev` now builds with `-tags collage_dev`, and
  `collage new` writes the `//go:embed` lines into `embed.go`, constrained with
  `//go:build !collage_dev`, beside an `embed_dev.go` that declares the same
  variables empty. Development mode already read both directories from disk, so
  nothing changes in what is served; `collage build`, `collage export` and
  `go build` embed as before. An existing project keeps growing the cache until it
  makes the same move, and `collage dev` says so when it starts, naming the file:
  take the `//go:embed` lines and their variables out of `main.go` into an
  `embed.go` that starts with `//go:build !collage_dev`, and add an `embed_dev.go`
  with `//go:build collage_dev` declaring `templatesFS` and `staticFS` as empty
  `embed.FS` values. `go clean -cache` reclaims what is already there.

## v0.45.0

### Added

- **ErrorEvent says which status a failure is answered with, and carries the request.**
  `ErrorEvent.Status` is the HTTP status written for the failure — 404 for a missing
  asset, 500 for a failed render or a recovered panic — or 0 when no response is
  written for it, as for a cache write that failed after the page was served. An
  `"error_page"` event, the application's own error page failing to render, always
  says 500. `ErrorEvent.Request` is the reader's request, for reading only (its body
  may already have been read), and nil when the failure did not come from a request.
  A page's `"cache_write"` failure happens in the render shared by every reader of
  one cache key, so its `Request` is that shared render's stripped request — no
  cookies, only the headers declared with `Vary`, no `RemoteAddr` — not a particular
  reader's; a document's carries the request of the reader whose miss started it.
  A plugin reporting failures elsewhere can now tell a 5xx from a 4xx and name the
  URL, method and headers without a middleware of its own. See
  [docs/plugins.md](docs/plugins.md).

### Changed

- **A panic recovered while serving a request reaches ErrorHook as a `*collage.PanicError`.**
  The error still wraps `collage.ErrPanic`, so `errors.Is(err, collage.ErrPanic)`
  holds as before; `errors.As` now also reaches the panic value and its stack, and
  `errors.Is` reaches the value itself when the code panicked with an error. The
  error's message changed: it no longer carries the stack, only
  `collage: panic recovered while serving the request: collage: panic: <value>`.
  The stack left the message for a separate `stack` attribute on the
  `collage: request failed` log line, so logs keep it. Anything that matched the
  stack in the message text has to read the `stack` attribute, or `PanicError.Stack`,
  instead.

## v0.44.0

### Added

- **`collage.SafeRedirect` checks a "next" before it becomes a redirect.**
  It returns `next` when it is a path on this site — it starts with "/", not with "//",
  holds no backslash and no control character, and still starts with a single "/"
  once cleaned — and `fallback` otherwise; a fallback that is not one either gives
  "/". It is for a login that sends the reader back to a "next" taken from a URL
  query parameter: `http.Redirect(w, r, collage.SafeRedirect(r.URL.Query().Get("next"), "/"), http.StatusSeeOther)`.
  The check is the one collage applies to its own redirects plus two rules, because
  `http.Redirect` cleans a rooted path: `/./\evil.com` would leave as `/\evil.com`,
  which a browser reads as `//evil.com`. So a backslash is refused anywhere, the query
  included (`/search?q=a\b` gives the fallback), and the cleaned path is checked too.
  An absolute URL is never accepted, even one on this site's own origin: the rule
  stays one simple check. collage itself still sends a guard's or an action's Location
  as given, since some must leave the site (a sign-in provider). **Core routes and
  guards still have no defaults:** a handler can redirect to any Location; only
  user-supplied input (a "next" parameter) passes through `SafeRedirect`. See
  [docs/routing.md](docs/routing.md#saferedirect).

## v0.43.0

### Added

- **A plugin can rewrite each HTML response after the cache: `PersonaliseHook`.**
  A plugin that implements `OnPersonalise(ctx, ev *collage.PersonaliseEvent) error`
  is called for every response on its way to one reader, with `ev.Request` (that
  reader's own request, not a shared render's stripped one), `ev.Header` (the
  response header), `ev.Body` and `ev.Personal`. A hook replaces `ev.Body`
  (assigns a new slice); it must never write into it: the slice may be the
  cache's, shared with concurrent readers. It runs in the core's personalise
  stage: after the reader's forgery token has gone into the shared body, inside
  every middleware, so before compression, and before the dev reload script; on a
  page it runs before the dev overlay, while on an error page, or a page an action
  answers with, the overlay is already in. Plugins run in registration order and
  each sees the previous body. It covers a page (from the cache or fresh), a
  fragment path or fragment read, an action's HTML answer and an error page; a
  document is not HTML and gets no call. A hook that makes the body particular to
  its reader sets `ev.Personal`: the ETag is recomputed from the body sent, a page
  answers `private, no-store`, a fragment read `private, no-cache` with the
  recomputed ETag, and an action's HTML `private, no-store`, overriding even a
  Cache-Control the handler set. A body changed without `Personal` still gets an
  ETag naming what was sent. A hook that fails or panics
  is logged and the page, fragment or action answers 500; on an error page the
  built-in page for that status is sent instead, never a half-personalised body.
  A body a hook made personal is never answered 304, from the cache or as a
  fragment read, not even to `If-None-Match: *`. **Nothing changes for a site
  without such a plugin.** A compressing middleware that keeps bodies per ETag
  gains nothing from a hook's personal response: its ETag is new every time. It is
  first used by
  elagoht/secure v0.2.0, which moves its CSP nonce here and so fixes a bug: with
  secure listed before elagoht/compress in `Config.Plugins`, the nonce marker was
  left in the compressed body and every inline script was blocked. See
  [docs/plugins.md](docs/plugins.md#rewriting-each-response-personalisehook).

## v0.42.0

### Added

- **`OriginResolver` is an optional plugin hook that gives each host its own
  public origin, and `collage.BaseURL(rc)` reads it.** A plugin that implements
  `Origin(ctx, host string) (origin string, ok bool)` says which origin
  (`https://acme.app.com`) absolute URLs for a host are built against. The first
  plugin in registration order that knows the host wins; the host is matched
  lower-cased with its port stripped, an IPv6 literal without its brackets; an
  origin that is not a bare `scheme://host[:port]` naming a host is ignored, and dev mode logs it once per plugin and
  origin; an unknown host gets `Config.BaseURL`. Application code calls
  `collage.BaseURL(rc)` for the request's origin. It is safe in a shared, cached
  render, because the host is part of the cache key, and a static build, which has
  no request, gets `Config.BaseURL`. A resolver that panics is logged and skipped.
  **Nothing changes for a site without a resolver.** See
  [docs/plugins.md](docs/plugins.md#which-origin-a-host-has-originresolver).
- **`Origins` is an optional capability of the `Host` a plugin receives.** Reach it
  by type assertion, `host.(collage.Origins)`: `OriginFor(ctx, host) string` is the
  origin for a host, a resolver's else `Config.BaseURL`, and `Dynamic() bool`
  reports whether a resolver is registered, so a plugin that builds absolute URLs
  knows whether the origin can differ by host. `plugin.Host` gains no method, so a
  test double that implements it still compiles.
- **`collage.VariedContext(ctx, header)` is `Varied` for code that holds only a
  context.** `StaticParams`, a sitemap's `LastMod` and a document handler's `ctx`
  have no `RenderContext`; they read a header declared with `collage.Vary` through
  this, and it works in the stripped context a shared render gets.
- **`collage.ParseOrigin(raw) (string, error)` is the rule `Config.BaseURL` is
  checked with, exported.** It accepts a bare `scheme://host[:port]` with scheme
  `http` or `https` and returns it without a trailing slash. A plugin that takes
  origins in its own configuration validates them with it rather than a copy.
  `collage.ErrInvalidBaseURL` is now the same error as `plugin.ErrInvalidOrigin`,
  with the same message, so `errors.Is` matches either.
- **`CacheInvalidateEvent.Entries` names the host of each dropped entry.** It is a
  list of `InvalidatedEntry{Host, Path}`, sorted by host and then path; a path
  cached under two hosts is two entries, and the host keeps its port if the
  request had one. `Paths` is unchanged. A plugin that purges a CDN or pings a
  search engine reads `Entries` to build each URL against the right origin. The
  host is recorded as a `collage:host:<host>` dependency tag beside
  `collage:path:`; host tags are exempt from the tracker's `MaxKeysPerTag`, and
  `MaxKeys` still bounds memory.

## v0.41.1

### Fixed

- **A cacheable document's shared render no longer sees per-reader context.**
  v0.39.0 stripped the request context's values from a cacheable page's one
  render, served to every reader of its cache key; a cacheable document — a feed,
  a sitemap, a JSON endpoint — still had them. A document handler that read a
  value the application's middleware stored in the context, directly or through
  `rc.Request`, read the first reader's, and that body was cached and served to
  everyone after. Its render now gets the same stripped context a page's does,
  and so does `OnDocumentRendered`; a value the document depends on is declared
  with `collage.Vary` and read back with `collage.Varied`, as for a page. A
  `Dynamic` document, rendered for its one reader, keeps the whole context.

## v0.41.0

### Added

- **A placeholder may have text around it: `/blogs/{slug}.md`, `/post-{id}`,
  `/v{version}.json`.** The segment must carry the text before and after the
  placeholder with at least one character between them, and what lies between is
  the value: `/blogs/hello.md` gives `slug` = `hello`. A page at `/blogs/{slug}`
  and a document at `/blogs/{slug}.md` stand side by side — a post and its
  Markdown, a feed per category at `/feeds/{category}.xml` — where before the
  second pattern was refused at registration. Links build with the value escaped
  and the text kept (`{{pageURL "post-md" "slug" "çay"}}` is
  `/blogs/%C3%A7ay.md`), and an export writes `blogs/hello.md` beside
  `blogs/hello/index.html`.

  Which route a segment reaches never depends on registration order. At every
  level a static segment is tried first, then placeholders with text around them
  from the most specific, then the bare placeholder, then the catch-all. Two
  placeholders at one position are disjoint (`{slug}.md`, `{slug}.json`: the text
  decides), nested (`{slug}.min.md` within `{slug}.md`, both within `{slug}`: the
  more specific wins), or crossing — some segment matches both and neither is more
  specific, as `a{x}` and `{x}b` both match `aXb` — which is refused with the new
  **`ErrOverlappingPattern`**, naming such a segment. The test for each is exact,
  so no registered pair is ever ambiguous. A placeholder with text around it
  never captures `.` or `..` — `/blogs/...md` would otherwise hand `{slug}.md`
  the slug `..`, which a handler joining it onto a directory climbs out with — so
  such a segment is the bare placeholder's, captured whole, and `BuildPath`
  refuses either value. A segment holds one placeholder
  (`{name}.{ext}` has no single answer for `a.b.c`), a catch-all takes no text
  around it, and there is no regex: a value is checked by its handler, which keeps
  every collision decidable at registration. See
  [docs/routing.md](docs/routing.md#text-around-a-placeholder).

## v0.40.0

### Added

- **`Cache.MaxBytes` caps what a built-in cache stores, in bytes.** `MaxEntries`
  counts entries, not what they weigh, and the cache key holds the query by
  default, so one large page asked for under thousands of invented queries
  (`?utm=1`, `?utm=2`, ...) filled the cache with copies of it: 665 MB of live
  heap on a documentation site, gigabytes for larger pages, reachable by an
  anonymous client. Past `MaxBytes` the oldest entries are evicted, as at
  `MaxEntries`, and a page larger than the whole cap is served but not stored.
  Zero means 256 MiB for `"memory"` and 1 GiB for `"disk"`; negative means
  unlimited. A custom `Store` ignores it.

- **`app.Register(items ...)` registers pages, documents and actions in one
  call**, in order, each through its own `Register` method, and stops at the first
  refusal with its error wrapped as `register page "name": ...`. It replaces the
  loop per kind a `routes.go` wrote; the scaffolded one is a single call now. It
  takes `collage.Registrable`, which only `*Page`, `*Document` and `*Action`
  satisfy; a `nil` is `ErrNilRegistrable`. The not-found and error pages keep
  `RegisterNotFoundPage` and `RegisterErrorPage`. See
  [docs/routing.md](docs/routing.md#registering-several-at-once).

- **`pkg/collagetest`: a test drives the application the way a browser does.**
  `collagetest.New(t, app.Handler())` is a client with a cookie jar of its own;
  `Get` reads a page, and `Submit(page, action, values)` submits the page's form
  with every hidden input it carries — the forgery token, and whatever a plugin
  stamps into a form, such as a honeypot's timestamp — and the test's values on
  top. A test fills in only the fields a reader would, and no longer reads the
  token out of the page with a regular expression or copies cookies between
  recorders by hand. Redirects are not followed, so a `303` and its `Location` can
  be checked; `Follow` takes one. `CSRFToken` is the page's token for a request
  that sends it in a header, and `Request`/`Do` make any other request. See
  [docs/testing.md](docs/testing.md).

- **`collage add page|fragment|action|document <[area/]name>`** writes a new page
  (and its content), fragment, action (builder and handler) or document into the
  project, in the scaffold's layout, and registers it in `routes.go` — after the
  last item of its kind in an `app.Register` call, or at the end of a
  `[]*collage.Page` literal — editing the file in place so its comments stay put.
  The path's locale is read from `main.go`'s `Locale.Default`; `--file` keeps a
  template in a file under `Template.Root`. Everything is worked out before
  anything is written: an existing file, a name the project already gives a page,
  action or document, or an identifier the package already declares refuses the
  whole command. It works on a project scaffolded by an earlier version too. See
  [docs/cli.md](docs/cli.md#collage-add).

- **`collage check` and `App.Check`: broken links found before anything
  renders.** Every template's `{{pageURL}}`, `{{pageURLIn}}`, `{{actionURL}}`,
  `{{fragmentURL}}`, `{{fragmentURLIn}}` and `{{localeURL}}` written with literal
  names is checked against what is registered, with the framework's own URL
  builders: an unknown name — with the closest registered one suggested —
  parameters that do not fill the pattern, a locale no URL carries, a route with no
  path in the named locale. Each is a finding with its template, line and column,
  inline templates included. `collage check` exits 1 on any, for CI; `-json` is
  for an editor. It is `go run . collage-check`, answered by
  `collage.DispatchCommands`: a project's own `main.go` needs no change, but its
  collage must be this version — an earlier one answers `unknown command:
  "collage-check"`. See [docs/cli.md](docs/cli.md#collage-check).

### Changed

- **`collage new` scaffolds a different layout**: `pages/<area>/<name>.go` beside
  `fragments/pages/<area>/<name>.go`, `fragments/layouts` with `Master()`,
  builders in `actions/<area>.go` and their handlers in `actions/funcs/`, and state
  in `data/<domain>/`. Small fragments keep their markup inline. It is the layout
  `collage add` writes into. A project scaffolded before is unchanged and keeps
  working; nothing in the framework depends on the layout.

- **The demo project's tests use `collagetest`.** `collage new --template demo`
  writes a `main_test.go` without its own `get`, `post` and `token` helpers. An
  existing project's tests are unchanged and keep working.

- **Renders no longer cost as much as the project's whole template set.** Every
  fragment of every render cloned all of the application's templates and escaped
  the copy again. A page of five fragments in a project of 200 templates took
  2.2ms and 2.9MB per request; it now takes 62us and 42KB, and every dynamic render
  is 40-66% faster. A render reuses an executed copy and puts back every function
  it bound before another render takes it, so nothing bound to one request
  reaches the next.
- **Benchmarks and stress tests.** `go test -bench=.` measures each request path a
  site serves, and the suite now checks under concurrency that a crowd on a cold
  page renders it once, the disk cache stays inside its cap, abandoned or
  panicking requests leave nothing running, and shutdown finishes the requests in
  flight (skipped with `-short`). A tag's CI compares its benchmarks with the
  previous tag's and reports the difference in the job summary.

### Fixed

- **A disk cache eviction no longer stalls every writer.** The eviction scan read
  the whole cache directory under the lock every write takes, and each entry's file
  was written under it too, so under a flood of new keys (`?utm=1`, `?utm=2`, ...)
  every writer queued behind a ~60ms scan. Files are now written before the lock
  and only renamed under it, and the scan runs outside it. A new entry that reaches
  a full cache while a scan is still making room is served but not stored, so
  `MaxEntries` still holds.
- **The dependency tracker is bounded across pages, not only per tag.** Nothing
  removes a key from the tracker when the cache evicts its entry, and every cached
  path is a tag of its own, so `MaxKeysPerTag` alone let the tracker reach 10000
  keys per page while the cache stayed at `MaxEntries`. That is an out-of-memory an
  anonymous client can cause on a large site. With a built-in cache the tracker now
  holds at most `MaxEntries` keys in all, dropping the oldest written first. A
  custom `Store` keeps the per-tag bound only.

## v0.39.2

### Fixed

- **A fragment path answers 404 when its required fragment is not found.** A
  `Required()` fragment whose data handler wraps `collage.ErrNotFound` made its page
  a 404 but its fragment path — and any action answering with that `Fragment` — a
  500. Both are 404 now, so "missing" and "broken" are told apart at a fragment's
  own URL as they are on the page. The body is unchanged: plain text, never cached,
  as for any action's failure.

## v0.39.1

### Fixed

- **A test file was not `gofmt`-clean**, which failed the release check. No change
  to the library or its behaviour; `elagoht/secure` v0.1.4 pairs with this to make
  a header turned off there override v0.39.0's baseline.

## v0.39.0

### Breaking

- **A cacheable page's shared render no longer sees per-reader context.** One
  render of a `Static`, `Incremental` or `Shared` page is served to every reader
  whose request has its cache key. That render already had the reader's cookies,
  headers, body and address stripped from it; it kept the request context's
  values, so a data handler that read a value the application's middleware had
  stored — a signed-in user, a tenant, a request id — read the first reader's, and
  it was then cached and served to everyone after. The context is now stripped to
  the framework's own request-independent values (the route and the vary set) for
  a shared render, the way the rest of the request already was.

  What to do: a middleware that made a value available to a cacheable page's data
  handler by putting it in the context — `collage.Vary(r, "Accept-Language", lang)`
  followed by `r.WithContext(context.WithValue(...))` — now declares it with
  `Vary` alone and the handler reads it with the new `collage.Varied(rc, header)`.
  The value is safe to read there precisely because it is in the cache key, so
  each value has its own cached page; a value that is not a cache dimension does
  not belong in a shared render, and its page should be `Dynamic`. In development,
  a hidden value a handler actually reads is logged. A dynamic page, and a handler
  reading the context off the request on a page that is not cached, are unchanged.

### Added

- **`collage.Varied(rc, header)`** returns the value a middleware declared for a
  request header with `collage.Vary`, and whether one was declared. It is how a
  data handler reads a cache dimension on a cacheable page — from the cache key,
  where reading is safe — rather than from the request context.

- **Baseline security headers on every response.** A page, a cached hit, an
  action and a mounted asset now carry `X-Content-Type-Options: nosniff` and
  `X-Frame-Options: SAMEORIGIN` unless the application turns them off, so a site
  that has not installed the `elagoht/secure` plugin still has MIME-sniff and
  clickjacking protection on its forms. `Security.FrameOptions` sets the value
  (`"-"` sends none, empty is `SAMEORIGIN`, anything else is verbatim) and
  `Security.NoSniff` turns nosniff off when it points at `false`. A plugin that
  sets its own overrides these; the full Content-Security-Policy, HSTS and the
  rest remain what `elagoht/secure` adds.

- **`Config.BaseURL` and `Host.BaseURL()`**: the site's own public origin —
  `"https://example.com"` — in one place, for the absolute URLs a render cannot
  otherwise know: a canonical link, an `og:url`, a sitemap's entries, a feed's self
  link. It is distinct from `Server.Host`, the address the process listens on, which
  behind a proxy is a loopback or a container name, not the origin a reader typed.
  It must be a bare origin — a scheme and a host, no path, query or fragment — and
  `Validate` refuses anything else with `ErrInvalidBaseURL`; an empty value is fine,
  for a site that builds no absolute URLs. A plugin reads it with `host.BaseURL()`
  (reported without a trailing slash) in `Configure` or `Init`, rather than taking
  its own copy, so a site configures its origin once.

### Changed

- **Forgery tokens expire.** A CSRF token now carries the time it was issued,
  signed alongside its nonce, and is refused once it is older than
  `Security.CSRFTokenTTL` — twelve hours by default. A reader with a long-open
  form is issued a fresh token rather than refused, so ordinary use is unchanged;
  what changes is that a token that leaks can no longer be replayed indefinitely.
  Set `CSRFTokenTTL` to a negative value to keep the previous behaviour, a token
  valid for as long as its signature.

- **The disk cache is bounded.** It honours `Cache.MaxEntries` — the cap the
  in-memory cache already used, ten thousand by default — and evicts the oldest
  entries by file modification time once it is reached, so an anonymous caller
  varying the request `Host` or query (both cache-key dimensions) can no longer
  fill the disk one never-evicted file at a time. A disk cache that should be
  unbounded sets `MaxEntries` to a negative value, deliberately.

## v0.38.0

### Breaking

- **A plugin's middleware runs where the plugin was registered.** The plugins in
  `Config.Plugins` are now outside every middleware `app.Use` adds; they used to
  be inside it, because they add theirs in `Init`, which runs after the
  application's registrations. So application middleware could not read what a
  plugin puts in the request's context: `session.FromContext` was `nil` in it, and
  loading the signed-in user once per request, in middleware, could not be
  written. Now it can. A plugin registered with `app.RegisterPlugin` sits among
  the application's middleware where the call is made: inside what `app.Use`
  added before it, outside what it adds after.

  What to do: a plugin that reads a value the application's middleware puts in the
  context — a rate limit whose `KeyFunc` reads the signed-in user, say — now runs
  before that middleware and finds nothing. Register it with `app.RegisterPlugin`
  after the `app.Use` it depends on, rather than in `Config.Plugins`. A plugin that
  adds template functions cannot be registered that way; none of the plugins with
  middleware that read the application's context does.

  Also visible: a plugin's middleware now sees a request the application's own
  middleware answers, a `401` from an auth check, and the time it spent.

## v0.37.1

### Changed

- **The development overlay is a dialog in the middle of the page**, not a panel
  along the bottom. It goes in last, after anything a plugin put before
  `</body>`, so it sits above a plugin's own panel, such as the devtoolbar's, rather
  than under it. It has two buttons: minimize turns it into a button at the bottom
  right that counts what it holds (`1 failure · 2 findings`) and opens it again,
  and close removes it. Clicking outside the dialog minimizes it too. Its styles
  are scoped to it, so the page's CSS does not reach it.

## v0.37.0

### Added

- **`rc.URL(name, params)` and `rc.ActionURL(name, params)`**: a page's or an
  action's URL by name from Go, in the render's own locale, falling back to the
  default one as `{{pageURL}}` and `{{actionURL}}` do. They work in a data
  handler and in an action's handler, so an action redirecting by name, or a
  fragment building a canonical URL, no longer needs the `*App` passed to it.
  They build through `App.URL`'s path builder: a value is one escaped path
  segment, and one holding `/` or being `.` or `..` is `ErrRouteParams`, so a
  value from the request cannot make the path another site's URL. On a
  `RenderContext` collage did not make, both return `ErrUnknownRoute`.

## v0.36.0

### Added

- **`{{actionURL "logout"}}` and `App.ActionURL(name, locale, params)`**: an
  action's URL by name, as `pageURL` and `App.URL` give a page's. A form posting
  to an action on its own URL no longer writes the path. It takes parameters as
  `pageURL` does and falls back to the default locale in a template. A page's own
  action is under the name it was registered with, `"story:POST"` when
  `WithAction` gave it none. Actions keep a namespace of their own, apart from
  pages, because a `login` page and a `login` action are the usual pair.
- **A contained fragment failure is logged**, as a warning through
  `Config.Logger`, in every mode. Such a failure is in a fragment that is not
  `Required`, with or without a fallback. The log line names the page, the
  fragment, the locale, whether a fallback covered for it, and the error. Before,
  outside dev mode, it showed only as a missing section on a page answering 200.

### Changed

- **`pageURL`, `pageURLIn`, `fragmentURL`, `fragmentURLIn` take integers and
  `fmt.Stringer`s as parameters**, not only strings: `{{pageURL "story" "id"
  .ID}}` works for an `int64` ID. Before, it failed as the template ran,
  `expected string; got int64`. A float, a bool or nil is refused with
  `ErrRouteParams`, naming its type.

## v0.35.0

### Changed

- **A path in a locale no URL reaches is refused at registration**, with
  `collage.ErrLocaleUnreachable`. A locale is unreachable when it is neither
  `Locale.Default` nor in `Locale.Supported`, and under `DisablePathLocale` every
  locale but the default is. This applies to `RegisterPage`, `RegisterAction` and
  `RegisterDocument`. Before, such a path went into a tree requests never reach.
  A Turkish-only site that wrote `WithPath("tr", …)` and left `Default` at its
  `"en"` default started without complaint, then answered 404 at every URL. It
  now fails to start, and the error names the page and the locale config. The fix
  is `Locale.Default: "tr"`. `WithPath` now documents that its locale must be one
  of these.

## v0.34.2

### Fixed

- **A mounted file with no extension is typed from its content again**, as it was
  before v0.34.0 — an image uploaded as `/uploads/logo` is `image/png`, which
  elagoht/opti-image needs to convert it — but never into a type that runs: one
  that looks like HTML or XML is `text/plain`. v0.34.0 served every such file as
  `application/octet-stream`. The same holds in `collage serve`. `nosniff` is
  still sent with every mounted file.

## v0.34.1

### Added

- `collage.ErrCSRFCrossOrigin`, the refusal of a submission the browser marked as
  sent from another origin, and `collage.ErrCachedFetchPanicked`, what a `Cached`
  call waiting on a fetch that panicked is told — so an error hook can tell them
  apart with `errors.Is`. Both were added in v0.34.0 without a name outside the
  framework.

### Fixed

- A mount test assumed `.bak` has no type; on Linux `/etc/mime.types` gives it
  one, and CI failed on the v0.34.0 tag. The framework was not affected.

## v0.34.0

A security release. Every collage site should take it.

### Breaking

- **A fragment path meets the guards of the page that opened it**, outermost
  layout inwards, then the fragment's own — over HTTP and through
  `RenderFragment`. It ran the fragment's own guard alone, so a guarded layout
  whose content was opened at a URL of its own served that content to anyone.
- **An encoded slash (`%2F`) in a path reaches no route**: it is a 404.
  Middleware reads the decoded `r.URL.Path`, where `/public%2Fsecret` is two
  segments, and the router read one, so a middleware letting `/public/` through
  let the request through to `/{slug}`. `BuildPath` and `{{pageURL}}` refuse a
  `/` in a single segment's value; a value that is a path belongs in a
  catch-all, `{rest...}`. This also stops a static build slug of `../about`
  writing over another page.
- **A cacheable page or document renders from only what its cache key holds**:
  the path, the host, the query parameters in the key, and the headers declared
  with `collage.Vary`. A handler on one no longer sees the reader's cookies,
  `Authorization`, address or client certificate, or a query parameter
  `WithCacheParams` left out — it used to see the first reader's, and could write
  them into the copy every later reader was served. A page that reads them is
  dynamic. The host is now part of the cache key, whose prefix is `v2:`.
- **A request the browser marks as cross-origin is refused** by the forgery
  check, even with a valid token: `Sec-Fetch-Site`, or `Origin` against `Host`.
  A form on another origin that posts here is named in the new
  `Security.CSRFTrustedOrigins`.

### Fixed

- **Open redirect**: `GET /./%5Cevil.com` answered `301 Location: /\evil.com`,
  which a browser follows to `evil.com`. The clean-path redirect writes its path
  escaped. A locale redirect no longer begins with `//` either.
- **Forgery tokens could be harvested**: an error page served the raw token
  marker, which was replaced wherever it occurred — planted in a reader's comment,
  it came back as each reader's token. Error pages are personalised like any page,
  and only the value `{{csrfToken}}` renders is replaced.
- **Disk exhaustion**: a multipart body's temporary files were never removed —
  accepted or refused, and a refusal needed no token. They are removed after
  the action answers, and a cookie the guard never signed is refused before the
  body is read.
- A `Cached` fetch that panicked held its key until a restart, and one that
  failed because its first caller went away failed every caller sharing it.
- A guard's answer carries `Cache-Control: no-store`, so a CDN does not serve
  one reader's refusal or login redirect to the next.
- A mounted file with no known extension is `application/octet-stream`, not
  sniffed, and every mounted file carries `X-Content-Type-Options: nosniff`.
  Dotfiles other than `.well-known` are neither served nor built.
- `collage dev` answers only a `Host` naming this machine — `localhost`, an IP
  address, or its `HOST` — against DNS rebinding.
- The terminal log handler quotes an attribute holding a control character, so a
  request path cannot forge a log line or rewrite the terminal.
- The scaffold's `.gitignore` covers every `.env.*` but `.env.example`.

Found by porting Next.js's own test suite, whose passing properties now stand as
regression tests (`nextjs_*_test.go`):

- **A request body is bounded before the middleware reads it**, at the limit of
  the action it routes to. A middleware that read or parsed the body read it
  unbounded, and left the action's own limit nothing to bound.
- **A redirect's captured value cannot add a query parameter**: one substituted
  after the destination's `?` is query-escaped, so `/login?next={slug}` cannot be
  handed a second `next`. A registered redirect carries the request's query
  string, and one to `//host` or `/\host` is refused at registration.
- A path made dirty by an encoded slash is a 404, not cleaned into a route.
- A reader who closes the connection is logged at debug and reported to no error
  hook; a reader who left while waiting on another's render no longer panics.
- A degraded render is `no-store`, pages and documents are `no-store` in
  development, the framework's `Vary` is added beside a middleware's rather than
  replacing it, and a redirect shaped by `Collage-Fetch` names it in `Vary`.
- `CSRFTrustedOrigins` entries match whatever their case or default port, and a
  wildcard is refused. The forgery cookie is `Secure` behind a proxy that sends
  `X-Forwarded-Proto` as a list.
- `collage serve` serves no dotfile and sniffs no type.

## v0.33.0

### Added

- **`rc.Page` in an action's handler is the page whose URL it answers on**, so a
  refused form answers with `Page: rc.Page`. A page's own action no longer needs
  the `var page` declared before the page and captured by the handler — nor the
  ordering that made it work. An action at a URL of its own has a `nil` `rc.Page`.
  A fragment an action answers with is now rendered within that page too, so
  `{{localeURL}}` works in it and its render metric is named `page/fragment`.
- In dev mode, an action answering a form post with a bodiless 422 logs a
  warning: to the reader it is a blank page. Requests carrying `Collage-Fetch` are
  exempt.

### Changed

- The demo scaffold's `/hello` action is a plain function answering with
  `rc.Page`.

## v0.32.0

### Changed

- **`collage new` scaffolds the minimal template by default.** One layout, one
  page, a stylesheet — nothing to delete before starting. The demos are
  `--template demo`.
- **`collage build` targets this machine by default**, not linux/amd64, so the
  binary it writes runs where it was built. For a server, name the target:
  `collage build -os linux -arch amd64`.
- **The default port is 6060**, not 3000: `Config.Server.Port`'s default, the
  scaffolded `main.go` and `.env.example`, and the address `collage dev` listens on
  when nothing sets `PORT`. A project that sets `PORT` is unaffected.

### Fixed

- `collage dev` logs are readable again. Since it became a proxy, the program's
  stderr has been a pipe — read for the error page — so the program logged in
  slog's plain text format. On a colour terminal it now runs the program with
  `FORCE_COLOR=1`, which the default logger honours, and its own lines take the
  same shape: time, coloured marker, message, dimmed attributes. The error page
  shows the output with the escape codes removed. `NO_COLOR` still turns it off.
- Under `collage dev`, the program's `collage: listening` line names the address
  to open — `collage dev`'s own — rather than the loopback address the program
  was given behind it.

## v0.31.0

### Added

- **`BeforeActionHook`**: `OnBeforeAction` runs before an action's handler, after
  the page's guards, the action's body limit and the forgery check. A plugin
  checking a submission — a spam filter — reads it through the action's own limit
  with `ev.Form()`, which leaves it parsed for the handler, and answers in the
  handler's place by setting `ev.Result`. An error fails the request with `413`
  when it wraps `*http.MaxBytesError`, and `500` otherwise; the stage is
  `"before_action"`.

## v0.30.1

### Fixed

- The development reload script and overlay, and a plugin's `Hoist("head", …)`
  after the page was rewritten, no longer land in the middle of a tag on a page
  with non-ASCII text before `</body>` or `</head>`. The tag was found in a
  lowercased copy of the page, and `İ` (two bytes) lowercases to `i` (one), so
  each one moved the insertion a byte early: `</main<script>…></body>`.

## v0.30.0

### Added

- **`InlineHTML`**: a name for `string` to declare an inline template as a
  constant of its own (``const form collage.InlineHTML = `…` ``), which editor
  tooling colours as HTML.

## v0.29.0

### Added

- **`NewInlineFragment(name, html)`**: a fragment whose template is a string
  rather than a file, rendered and checked at registration exactly like a file
  template — slots, hoist, template functions, `{{template}}` calls into partials.
  An inline template cannot `{{define}}` templates of its own
  (`ErrSourceConflict`). `ErrConflictingTemplate` refuses a fragment naming both a
  file and an inline template. `collage inspect` reports `inline: true` for one.

## v0.28.0

### Added

- **`WithLayouts`**: a page's layout chain, outermost first. Registration folds
  the chain — the content fragment into the innermost layout, each layout into
  the next outer one — so a layout with a hole is a finished fragment, and
  nesting layouts is a list rather than nested builders.
- **`WithGuard`** on fragments (`collage.GuardFunc`, `collage.GuardDecision`): a
  guard runs for every page whose spine the fragment is on — its layout chain
  and its content fragment — after routing, before the page's cache is read and
  before `PageResolved`, and it covers the actions on the page's own URL. A
  fragment path runs its fragment's own guard and inherits nothing; standalone
  actions and error pages run none. A failing guard is reported at the new
  `"guard"` error stage.
- `collage inspect` reports `layouts` (outermost first) and `guards` per page.

### Changed

- **Breaking:** `WithLayout` is removed; a single layout is `WithLayouts(layout)`.
  `InspectedPage.Layout` is replaced by `Layouts`.
- Registration refuses a layout chain whose outer layouts arrive with a filled
  `content` slot (`ErrSlotOccupied`).
- `collage inspect` lists a layout shared by several pages once, rather than once
  per page.

## v0.27.0

### Added

- **`App.Inspect()`** and **`collage inspect`**: what an application is made of,
  as data — pages with their patterns and parameters, fragments with their
  templates and slots, documents, actions, template functions, plugins, locales,
  and the files mounts serve (`collage.Inspection`). `DispatchCommands` answers
  `collage-inspect` (`collage.InspectCommand`) with it as JSON, so
  `go run . collage-inspect` works in a scaffolded project; `collage inspect` runs
  that.
- **`collage.json`**, a plugin's description of itself for editors — template
  functions, attributes, snippets and its configuration's schema — documented in
  the plugins guide.

## v0.26.0

### Added

- **`collage.RouteInfo(ctx)`**: what a request resolved to — kind, registered
  name, the path pattern it was registered with (`/blog/{slug}`, no locale prefix)
  and the locale — for pages, documents and actions alike, so a span's
  `http.route` or a metric's label can be the pattern for every kind of route.

## v0.25.0

### Added

- **`RequestHook`**: `OnRequest` runs before collage's request span, middleware and
  routing, returns the context to serve the request under and a function told the
  status. A tracing plugin makes the caller's trace the parent of collage's span.
- **`collage.RouteOf(ctx)`**: what a request resolved to — its kind and the
  registered name or prefix — from the context `Metrics.HTTPResponse` and a
  request hook receive, so metrics and spans are labelled by route, not raw path.
- **`AfterRenderEvent.Hoist(area, key, html)`**: a plugin adds to a hoist area
  after the render, where the layout put `{{hoist}}`; a key the render declared is
  left alone, and `"head"` falls back to before `</head>` when an earlier plugin
  replaced the HTML.

### Changed

- The request span ends before `Metrics.HTTPResponse` is reported rather than when
  `ServeHTTP` returns.

## v0.24.0

### Added

- **`Host.BuildID()` and `App.BuildID()`**: the build serving —
  `Config.Cache.Version`, or a fingerprint of the executable — for a plugin
  versioning what a browser keeps across deploys.
- **`Host.ServeStatus(w, r, status)` and `App.ServeStatus`**: a plugin answering a
  request itself serves the site's not-found page for 404 and 410 and its error
  page otherwise, rather than a line of text.
- **`AfterRenderEvent.Fragments` and `AfterRenderEvent.DependencyTags`**: each
  fragment's time and failure (`collage.FragmentReport`), and the tags the render
  depended on.
- **`Handle` takes an exact path.** A prefix without a trailing slash, `/metrics`,
  answers that path alone.

### Fixed

- **Paths are cleaned before anything reads them.** A path with dot segments or
  doubled slashes is redirected to its clean spelling — 301, or 308 for a method
  with a body — ahead of middleware and plugins, so `/_collage/../admin` cannot
  walk past a check on `/_collage/`.
- **A static build starts the application first**, running every plugin's Init
  before pages are enumerated: a page a plugin registers, or the WithStaticParams
  of data a plugin loads, is built.

### Changed

- **Breaking:** `plugin.Host` has `BuildID` and `ServeStatus`; a test double needs
  them.

## v0.23.0

### Added

- **`CacheInvalidateEvent.Paths`**: the URL paths of the cached pages and
  documents an invalidation dropped, for a plugin purging a CDN or telling a
  search engine what changed.
- **`collage.PathTag(path)`**: every cached entry depends on a tag naming the URL
  path it was rendered for, so `InvalidateTags(ctx, collage.PathTag("/blog"))`
  drops what is cached there.

### Changed

- Findings are shown over a page an action answers with and over an error page in
  development, as over any other page.
- The scaffold's `main.go` and READMEs point to the published plugins.

## v0.22.0

### Added

- **`BeforeRenderEvent.Static` and `AfterRenderEvent.Static`** say a page is
  being rendered for a static build, not for a request, so a checking plugin can
  run in a build and in development and stay out of a production server's way.

## v0.21.0

### Added

- **Findings.** A plugin checking a page's output reports what it finds with
  `AfterRenderEvent.Warn` and `Error` (`collage.Finding`, `FindingWarning`,
  `FindingError`). In development they are shown over the page; in a static build
  they are listed in `BuildReport.Findings`, and an error-level one fails the build
  with `ErrBuildFindings`. The page is served and written either way.
- **`BuildFinishedHook`**: `OnBuildFinished` runs once a static build has written
  every file, with each one's kind, URL path and place on disk
  (`BuildFinishedEvent`, `BuiltFile`), for checks across pages.
- **`ConfigHost.AddRenderFunc(name, factory)`**: a template function made anew for
  each render from its `*RenderContext` — a nonce, a translation in the render's
  locale.
- **`Host.Use`**: a plugin wraps every request, after the application's own
  middleware.
- **`Host.URL`, `Host.FragmentURL`, `Host.Locales`, `Host.PageURLs`**: where every
  page lives, by name, in every locale — `PageURLs` expands a pattern through its
  `WithStaticParams` (`collage.PageURL`). `App.Locales` and `App.PageURLs` too.

### Changed

- **Breaking:** `plugin.Host` and `plugin.ConfigHost` have new methods; a test
  double implementing either needs them.

## v0.20.0

### Added

- **`Collage-Fetch` / `Collage-Location`** (`collage.FetchHeader`,
  `collage.LocationHeader`): an action answering a request marked `Collage-Fetch`
  with a redirect answers `204` with the destination in `Collage-Location` instead.
  A form submitted with `fetch` followed the redirect, downloading the page, and
  then navigated to it and had it rendered again.

### Fixed

- **Development pages share one reload stream.** Every tab listens through a shared
  worker, `/_collage/reload-worker.js`, that holds one stream for all of them. Six
  development pages side by side — visible, so v0.18.1's hidden-tab rule did not
  apply — held the browser's six connections to the origin, and nothing else loaded,
  collage-live's stream included. Without a shared worker, a tab keeps its own
  stream and lets it go while hidden, as before.

## v0.19.0

### Added

- **`FragmentBuilder.Shared()`**: the fragment's data handler returns the same for
  every reader at one moment — it reads nothing that tells readers apart — though
  what it returns changes over time. Its render may be sent to many readers:
  `FragmentRender.Shared` counts it. Unlike `Static()` it leaves the page's
  strategy alone, so a page of measurements stays dynamic and is not exported with
  one moment's readings. `Static()` implies it.
- **`FragmentRender.ETag`**: the ETag a request to the fragment's path would be
  answered with for the same body, so a pushed copy and a polled one can be told
  apart, or recognised as the same.

## v0.18.1

### Fixed

- **A render several requests share outlives the request that started it.** It
  ran under that request's context, so a reader leaving — a closed tab, a stopped
  reload, a development page reloading twice — failed every data handler still
  running with "context canceled", and every request waiting on the render got a
  page with those parts missing. It now runs under the context's values without
  its cancellation, bounded by the fragments' own timeouts.
- **A cancellation is no longer reported as a timeout.** A fragment whose context
  ended above it — a request cancelled, a shorter deadline — failed with
  "execution exceeded 5s" though nothing ran for five seconds. Only the fragment's
  own timeout is reported as one now; anything else is "stopped by context", with
  its cause.
- **A hidden development tab lets its reload stream go.** A browser holds at most
  six connections to an origin over HTTP/1.1, across all its tabs, and every open
  development page held one: from the seventh tab on, pages and fragment requests
  waited for a free connection. The stream closes when the tab is hidden and
  reconnects when it is seen. The page carries the version it was served at, and
  the stream greets it with the current one, so a change made while it was hidden
  still reloads it.

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

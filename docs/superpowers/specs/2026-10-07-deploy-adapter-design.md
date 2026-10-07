# Deploy adapter: a static export behaves like the server on a static host

Date: 2026-10-07
Status: approved design, pending implementation
Roadmap: [v1.0 roadmap](../plans/2026-10-03-v1-roadmap.md), phase 2, A6
("per-document headers; BuildFinishedHook"), and the collage-redirects CRLF hole
listed there.

## Motivation

`collage build` writes pages, documents and assets to disk, and what a server
adds to each response at runtime is lost on the way:

- `Cache-Control` by strategy (internal/httpx/handler.go `setCacheHeaders`), a
  document's `Content-Type`, the baseline `X-Content-Type-Options` and
  `X-Frame-Options`, `immutable` on fingerprinted assets;
- every header a plugin's middleware sets: collage-secure's CSP,
  Referrer-Policy, COOP, Permissions-Policy;
- page and document redirects (`WithRedirect`, `WithPermanentRedirect`): the
  build never exports them at all.

Only collage-redirects writes a `_redirects` file, as a document, and a rule
given in Go or JSON config can carry `\r`/`\n` into it (only `?# \t` / ` \t` are
rejected in `newRule`), which injects lines. `BuildFinishedHook` exists, but its
`BuiltFile` has no headers, status, or redirects for a plugin to work from.

The goal: a site exported with collage behaves on a static host as it does on
collage's own server — the same headers, the same redirects — and says plainly
whatever the host cannot carry.

## Decisions

- **Hosts:** Netlify, Cloudflare Pages, Vercel and GitHub Pages.
- **Headers are captured, not declared.** The build serves every output path
  through the application's real handler in-process and records the response
  headers, so every middleware's headers come along with no plugin changes. A
  header that differs between two requests to the same path (a CSP nonce) cannot
  live in a static file: it is left out, with a warning.
- **One writer of redirect files.** The adapter owns every host's redirect file.
  collage-redirects stops writing `_redirects` and offers its rules to the build
  instead. A rule carrying a control character is refused where it is made and
  again where it is written.
- **One plugin, one target:** `elagoht/deploy` with `"target"`. No target: the
  plugin writes nothing and warns.

## Design

### 1. Core (collage v0.52.0)

**Header capture.** After the build has written every page, document and asset,
and before `BuildFinishedHook` runs, it requests each output path through the
application's `Handler()` in-process (no network), as a static export would be
asked for:

- `GET`, no cookies, no `Accept-Encoding`, `Host` from `Config.BaseURL` (or
  `localhost`), the path the file is served at (`/blog/` for
  `blog/index.html`).
- Each path is requested **twice**. A header whose values differ between the two
  responses is per-response (a nonce, a timestamp): it is dropped and a warning
  names the path and the header — once per header name, with a count of paths.
- Request-specific headers are always dropped: `Date`, `ETag`,
  `Last-Modified`, `Content-Length`, `Set-Cookie`, `Vary`, `Content-Encoding`,
  `Transfer-Encoding`, `Connection`, `Age`.
- `BuiltFile` gains `Status int` and `Headers http.Header` (the kept headers,
  canonicalised, values in order). A capture that fails (a non-2xx status for a
  file the build wrote) is recorded as is and warned; the build does not fail on
  it.
- Capture is on by default for every build; `collage build` gains no flag. Its
  cost is two in-process requests per file at build time and nothing at
  runtime.

**Redirects.** `BuildFinishedEvent` gains `Redirects []BuiltRedirect`:

```go
type BuiltRedirect struct {
	From   string // the path pattern, as registered: "/old/{slug}"
	To     string // the destination, as registered: "/new/{slug}"
	Status int    // 301, 302, 307 or 308 (Redirect.EffectiveStatus); 410 from collage-redirects
	Source string // "page:<name>", "document:<name>" or the plugin's name
}
```

collected from every registered page's and document's `Redirects` (in
registration order) and from plugins implementing a new optional interface:

```go
// RedirectSource is a plugin whose redirects a static export carries: the build
// asks it once, after every file is written, and hands the rules to
// BuildFinishedHook in BuildFinishedEvent.Redirects.
type RedirectSource interface {
	Redirects() []BuiltRedirect
}
```

Redirects are not per locale (the router matches them before pages, whatever the
locale); they are exported as registered.

**Validation.** A redirect's `From` or `To` containing `\r`, `\n` or any other
control character is refused:

- `PageBuilder.WithRedirect` / `WithPermanentRedirect` and the document builder's
  record `ErrInvalidRedirect` (new, wrapped with the page or document name and
  the offending field), so registration fails;
- a `RedirectSource` rule that fails the same check fails the build with the
  plugin's name.

Two redirects with the same `From`, or a redirect whose `From` is a path the
build wrote a file for, fail the build: on a host either would be decided by
file order or by the host's own precedence, not by the application.

### 2. Plugin `elagoht/deploy` (new repo Elagoht/collage-deploy, v0.1.0)

Configuration (`plugins-config.json` or `NewWith`), read with
`collage.PluginConfig`:

```json
{ "elagoht/deploy": { "target": "netlify" } }
```

`target` is one of `netlify`, `cloudflare`, `vercel`, `github-pages`; anything
else is an error at configure time naming the valid values. An empty target
writes nothing and warns once.

It implements `BuildFinishedHook` only; at runtime it does nothing.

**Header compaction**, before any writer runs, deterministic:

1. Headers identical on every file go into one `/*` rule.
2. Of the rest, files sharing a path prefix and identical remaining headers are
   merged into a `<prefix>/*` rule (longest common directory prefix, only when
   every file under it shares the values).
3. What remains is one rule per path.

Rules are ordered `/*` first, then by path. Whatever a host cannot hold after
compaction is reported as a warning naming the rules left out.

**Writers** (formats and limits re-verified against each host's current
documentation during implementation):

| Target | Headers | Redirects | Cannot carry → warning |
|---|---|---|---|
| `netlify` | `_headers` | `_redirects` | — |
| `cloudflare` | `_headers` (100 rules) | `_redirects` (2000 static + 100 dynamic; 301/302/307/308) | 410; over a limit |
| `vercel` | `vercel.json` `headers` | `vercel.json` `redirects` (301/302/307/308) | 410; an existing `vercel.json` in the output is a build error, never overwritten |
| `github-pages` | not written: one summary warning with the count of header names and paths lost | one meta-refresh page per literal `From` (`<link rel="canonical">`, `noindex`) | 410, the permanent/temporary distinction, patterned `From`s; also writes `.nojekyll` |

Patterned redirects are translated to the host's syntax: `{name}` → `:name`,
`{name...}` → `*` with the host's splat token (`:splat` on Netlify/Cloudflare,
`:name*` on Vercel).

Every value written is checked once more for control characters before it
reaches a file; one found is a build error naming its source (a defence in depth
behind the core's validation).

Compressed siblings that other plugins write (`.br`, `.gz`) are not in
`ev.Files` and get no rules; hosts compress on their own.

### 3. collage-redirects (v0.2.0, breaking)

- Stops registering the `/_redirects` document.
- Implements `RedirectSource`, handing its rules (410 included) to the build.
- `newRule` refuses control characters in `From` and `To`, for rules from Go,
  JSON or the file alike.
- README: on a static host, add `elagoht/deploy`.

## Testing

- **Core:** capture keeps a static header, drops the request-specific ones,
  drops and warns on a header that differs between two requests (a test
  middleware setting a random value), records the status of each file;
  `Redirects` collects page, document and `RedirectSource` rules in order; a
  control character in `WithRedirect`, the document builder and a
  `RedirectSource` rule is refused; a duplicate `From` and a `From` that is a
  written file fail the build.
- **Plugin:** golden output per target for one fixture site (shared headers,
  a fingerprinted asset directory, a document with its own Content-Type, a
  per-path exception, literal and patterned redirects, a 410); compaction is
  deterministic; Cloudflare over 100 header rules warns and names what was left
  out; Vercel refuses an existing `vercel.json`; GitHub Pages writes meta-refresh
  pages and `.nojekyll` and one summary warning; an unknown target fails.
- **End to end:** a small app using collage-secure and collage-redirects is
  exported through each target and the files checked.

## Release

- collage **v0.52.0**: `BuiltFile.Status/Headers`,
  `BuildFinishedEvent.Redirects`, `BuiltRedirect`, `RedirectSource`,
  `ErrInvalidRedirect`. A redirect with a control character now fails
  registration — called out under **Breaking** in the changelog.
- **Elagoht/collage-deploy v0.1.0**, then added to collage's CI matrix (the
  matrix commit lands after the plugin's first tag, as for every new plugin).
- **collage-redirects v0.2.0.**
- Framework `docs/` (deployment, static export), the docs site EN+TR
  (static-export, plugins), and the VS Code extension's bundled schema for
  `elagoht/deploy` (schemagen).

## Out of scope

Live deploys or host APIs; host-specific features beyond headers and redirects
(Netlify functions, Cloudflare Workers, Vercel rewrites); per-response values on
a static host; more than one target per build.

<!-- collage:begin v@@VERSION@@ — written by `collage ai init`; edits between these markers are lost the next time it runs -->
# Working in this Collage project

This is a Go web application built on Collage (github.com/Elagoht/collage):
HTML rendered on the server from pages, layouts and fragments. You have
probably not seen Collage before, so follow these rules over your own habits.
Each rule says why, so you can decide a case it does not name.
Docs: https://collage.furkanbaytekin.dev/en/docs/introduction/

## 0. Before writing code

- Create files with `collage add <kind> <[area/]name>`. Kinds: `page`,
  `fragment`, `action`, `document`, `layout`, `guard`, `middleware`, `command`.
  It writes each file where it belongs, and registers pages, documents, actions
  with a path, middleware and commands in `routes.go`; then fill in what it
  wrote. Why: the layout below is what `collage add` makes, and hand-made files
  drift from it. A layout needs `--file`: `collage add layout <name> --file`.
- Writing a file by hand anyway: open an existing file of the same kind in this
  project and copy its shape.
- Need something common (a message after a redirect, sign-in, validation, SEO,
  uploads)? Read section 6 first: a plugin probably does it already.
- After a change run `collage check` (it starts the app, so a broken template
  or route fails there, and it checks the links templates make) and
  `go test ./...`.

## 1. Where things go

| What | Where |
|---|---|
| a page: its path, layouts, content | `pages/<area>/<name>.go`, one page per file |
| a page's content | `fragments/pages/<area>/<name>.go`, one fragment per file, with its view type and data handler |
| a fragment for a slot (`collage add fragment`) | `fragments/<area>/<name>.go`, one per file |
| a layout | `fragments/layouts/<name>.go` + `templates/layouts/<name>.html` |
| actions: form posts, endpoints | `actions/<area>.go`, the area's `collage.NewAction(...)...Build()` constructors, each followed by its handler factory `<name>Handler() collage.ActionHandlerFunc` |
| guards, middleware, commands, documents | `guards/`, `middleware/`, `commands/`, `documents/`, one per file |
| state, queries, database access | `data/<thing>/` |
| form validation | `validations/<area>.go` |
| any other shared helper | `utils/` |
| registering routes | `routes.go` |

- Never mix kinds in one file: no page or fragment in an action file, no action
  in a page file. Why: every kind has one place, so the next person, and
  `collage add`, finds it there.
- An action's handler sits under its builder, in the same file, the way a
  fragment's data handler does. What it calls goes in `data/`, `validations/` or
  `utils/`.
- A form's checks live in `validations/<area>.go`, one function per form, e.g.
  `func Signup(v *validate.Validator)` declaring `v.Field("email").Required().Email()`;
  the handler calls `v := validate.Form(rc)`, then `validations.Signup(v)`.
- Never put helpers on pages, fragments or view types as methods.
- Every page, document and action with a path of its own is registered in
  `routes.go` with `app.Register(...)`. An action attached to a page with
  `WithActionFor` is registered with that page: do not register it again.
  `collage add action <area>/<name> --page <area>/<page>` writes one attached.

## 2. Templates and data

- Pages and fragments keep their template inline:
  `collage.NewInlineFragment("name", nameBlock)` with
  ``const nameBlock collage.InlineHTML = `...` ``. Layouts keep theirs in a file
  under `templates/layouts/`.
- A view type (`homeView`, `counterView`) is plain data: fields, no methods.
- Before adding a template function, use the built-ins: `json`, `pre` (debug
  output), `add` `sub` `mul` `div` `mod` `min` `max` (and `addf`… for floats),
  `trim` `replace` `contains` `split` `truncate`, `first` `last` `seq` `keys`
  `hasKey` `dig`, `withQuery` `pathEscape`, and Go's `index` `len` `printf`.
- A function every template may call, that does not depend on the request, goes
  in `Config.Template.Funcs` in `main.go`. Anything that depends on the request
  is computed in the fragment's data handler and put into its view.
- Link with `{{pageURL "name"}}` and reference files with
  `{{asset "/static/x.css"}}`. Never hard-code a URL another route owns.

## 3. A layout changes every page under it

- `master` (`fragments/layouts/master.go`, `templates/layouts/master.html`)
  wraps EVERY page. Before editing any layout, find the pages that use it:
  `grep -rn "layouts.Master()" pages/`.
- Something only some pages need (flash messages on the sign-in pages, a
  panel's sidebar) goes in a nested layout: `collage add layout auth --file`,
  then `WithLayouts(layouts.Master(), layouts.Auth())` on those pages. Never in
  `master`.
- After changing a layout, open a page you were NOT working on and check it
  still looks right.

## 4. Head, CSS and titles

- In a layout's `<head>`, `{{hoist "head"}}` comes after the site stylesheet,
  and no stylesheet `<link>` comes after it. Why: the CSS fragments hoist must
  come last, so it wins over the site's.
- A fragment's own CSS: `{{stylesheet "/static/x.css"}}` in its template, or
  `if err := rc.HoistStylesheet("/static/x.css"); err != nil { return ..., err }`
  in its data handler. Never a `<link>`
  in a layout for one fragment's sake.
- Titles and meta tags: `WithTitle("...")` on a fragment, or `rc.HoistTitle`,
  `rc.HoistMeta` in a data handler. Never write a `<title>` yourself.

## 5. Actions, guards, redirects

- Signed-in readers only: `session.RequireUser("/login")` or
  `session.RequireUserPage("login")` from `elagoht/session`; do not write that
  guard. Any other policy is a `collage.GuardFunc` in `guards/`
  (`collage add guard admin`).
- A guard protects pages from their layout or content fragment:
  `collage add page panel/users --guard admin`, or
  `collage add layout panel --guard admin --file` for every page in it.
  A guard on a slot's fragment is ignored.
- An action attached with `WithActionFor` meets its page's guards first. An
  action with a path of its own meets ONLY its own guard: give it
  `WithGuard(guards.X)` unless anyone at all may call it. Documents have no
  guards: protect a private one with middleware.
- A guard says who may come in. Whether this user may change this record (their
  own recipe, their own task) is checked in the handler, before the change.
- A refused form renders the page again; an accepted one redirects with
  `collage.SeeOther(...)` or `collage.SeeOtherPage(rc, "name", params)`. A
  redirect target taken from the request goes through
  `collage.SafeRedirect(next, "/")`.
- An action that changes what a cached page shows sets `InvalidateTags` on its
  result, naming tags that page declares with `WithDependencyTags(...)` (or its
  data handler reports); a tag no page carries drops nothing.
- Never `WithoutCSRF()` on an action a browser form posts to. Every
  `<form method="post">` contains `{{csrfToken}}`.

## 6. Use a plugin; do not write it yourself

Add with `collage plugin add <name>` (several at once, or a `@bundle`). It
prints the plugin's next steps: follow them. `plugins.go` lists what this
project already has.

| You need | Add | Instead of writing |
|---|---|---|
| a message shown once after a redirect | `elagoht/flash` | cookie or session message code |
| a signed-in user, a session cookie | `elagoht/session` | your own signed cookies |
| sign-in with Google, Microsoft, GitLab | `elagoht/oauth` | an OAuth flow |
| a password on a whole staging site | `elagoht/basicauth` | an auth middleware |
| form validation, errors shown in the form | `elagoht/validate` | if-chains in handlers |
| form spam protection | `elagoht/honeypot` | a CAPTCHA |
| session, flash, validate and honeypot together | `@forms` | |
| file uploads | `elagoht/uploads` | multipart parsing |
| translations | `elagoht/i18n` | string maps per locale |
| parts of a page kept current live | `@live` | polling or websocket code |
| swapping a fragment in from JavaScript | `elagoht/client` | fetch-and-replace code |
| scheduled or background jobs | `elagoht/jobs` | goroutines with tickers |
| robots.txt, sitemap, meta tags, structured data, icons | `@site` | hand-written tags and files |
| a blog: `@site` plus feed, contents, share images | `@blog` | |
| rendering Markdown text, front matter into your own type | `elagoht/markdown` | a hand-wired Markdown renderer |
| code highlighting | `elagoht/highlight` | |
| search on a static site | `elagoht/search` | |
| resized, modern-format images | `elagoht/opti-image` | |
| bundling JavaScript, TypeScript, CSS | `elagoht/bundle` | a separate build step |
| production basics: health, security headers, compression, minifying, access log, rate limit | `@prod` | |
| banning clients that probe or brute-force | `elagoht/fail2ban` | |
| metrics and error reports | `@ops` | |
| tracing | `elagoht/otel` | |
| cookie consent, analytics | `elagoht/consent`, `elagoht/analytics` | |
| redirects kept in a file | `elagoht/redirects` | |
| one site served on many customers' hosts | `elagoht/tenant` | |
| pages readable offline | `elagoht/offline` | a service worker |
| tell a CDN or search engines what changed | `elagoht/cdnpurge`, `elagoht/indexnow` | |
| static-host headers and redirects from a build | `elagoht/deploy` | |
| HTML checks, a dev panel | `elagoht/htmlcheck`, `elagoht/devtoolbar` | |

Libraries, added with `go get` rather than `collage plugin add`: pagination,
`github.com/Elagoht/collage-paginate`; a Redis shared cache,
`github.com/Elagoht/collage-redis`.

When no plugin fits, write it in this project, following sections 1 to 5.

## 7. Never

- `any` in your code: Collage's API is typed; use the real types.
- Edit the text between the `collage:begin` and `collage:end` markers: add
  project rules under "Project rules" below.
<!-- collage:end -->

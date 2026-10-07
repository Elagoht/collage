# The CLI and static builds

```
go install github.com/Elagoht/collage/cmd/collage@latest
```

```
collage new <name> [--template minimal|demo] [--dir path] [--module path] [--force]
collage add <page|fragment|action|document> <[area/]name> [--file] [--path p] [--name n] [--locale l] [--type t]
collage dev
collage build [-o path] [-os name] [-arch name] [-i]
collage export [-out dir] [-clean]
collage serve [-dir dir] [-host name] [-port n]
collage inspect
collage check [-json]
collage version
collage help [command]
```

Exit codes: `0` on success, `2` for a usage error (no command, an unknown
command, a malformed plugin command), `1` for a command that parsed but failed.

## `build` and `export`

They produce the two different things a project can be deployed as.

```
collage build     # -> bin/<name>, the binary you run on a server
collage export    # -> dist/,      static files you put on a static host
```

`collage build` runs the `go build` somebody would otherwise have to remember:

```
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"
```

CGO off, because collage and the standard library need no C and a static binary is
what can go into an image holding nothing else. `-trimpath`, so the binary does not
carry the paths of the machine that built it. `-s -w`, which is most of the size.

**The default target is this machine**, so the binary runs where it was built. A
server is often something else — a binary built on a Mac does not run in a Linux
container — so name the target there: `collage build -os linux -arch amd64`. `-o`
changes where it lands.

`collage build -i` asks whether to write a `Dockerfile` and a systemd unit **beside
the binary**, in `bin/`. They are generated files, and the project root is for what
a person wrote. The one thing that costs is a flag at the other end, which the
output prints rather than leaving you to work out:

```
✓ bin/app
    linux/amd64 · 9.3 MB
    wrote bin/Dockerfile    docker build -f bin/Dockerfile .
    wrote bin/app.service
```

Without `-i` only the binary is written, and neither file is ever written over one
that is already there — these are files a project edits, and a build command that
replaces one with a default is one that quietly undoes somebody's work.

A scaffolded project's `.gitignore` ignores `bin/<name>`, the binary, rather than
`bin/` itself. That is what makes editing `bin/Dockerfile` safe: it is committed
like any other file, and only the build output is ignored.

The binary goes to `bin/` and the static site to `dist/` deliberately: `collage
export -clean` removes `dist/`'s contents, which would delete a binary sitting in
it.

## `serve`: looking at an export before deploying it

```
collage export
collage serve          # http://localhost:4000
```

Opening `dist/index.html` from the file system does not work: a `file://` page has
no root, so every absolute link and stylesheet in the export is broken. A plain file
server is closer but is still not a static host. This one behaves like one:

- **A directory answers with its `index.html`, or 404s.** No listings — no static
  host shows one, and showing one puts the shape of an export in front of anyone who
  asks.
- **`/about` resolves to `about/index.html`**, which is what `collage export` writes
  and what every static host looks for.
- **`404.html` is served with a 404.** `collage export` writes one from the page
  registered with `RegisterNotFoundPage`, so this is the site's own 404, not a
  default.
- **Nothing is cached.** The point is to look at what was just exported, and a
  browser holding the previous one is what stops that.

Port 4000 rather than 6060, so this and a project under `collage dev` can be up at
once — which is exactly when somebody compares them. `-dir`, `-host` and `-port`
change the rest.

It serves files and does not run the project. For that, `collage dev`.

Running it before exporting is the usual mistake, so an empty or missing directory
says what to run rather than serving a site that looks broken.

## Reading a build's output

`collage.PrintBuildReport` writes what a build did, and what it writes is shaped
around the thing that is easy to miss:

```
✓ 3 files written
    dist/index.html
    dist/static/app.css
    dist/static/app.41014ebb.css

▲ 1 skipped
    hello  page uses the dynamic render strategy, which cannot be built statically

3 written · 1 skipped · 0 failed · 2.4ms
```

Written files are summarised — ten of them, then a count, because a build that wrote
three hundred must not bury the one it skipped. Skips and failures are never
truncated: a skip is the build telling you a page is not in its output, and that is
what a build is for saying. The last line carries every count and is coloured by the
worst of them, so it answers "how did that go" without being read.

A **warning** is a page that was written but is not the whole of what the server
answers: one that declared, with `WithCacheParams`, which query parameters it reads.
A static host answers `/blogs?page=2` with the `/blogs` file, so pagination and
filters look like they work and do not — the export writes the page without a query
and says so, rather than letting it pass for a working archive.

Colour and the ✓ ▲ ✗ markers appear only on a terminal, and not when `NO_COLOR` is
set or `TERM` is `dumb`. Piped to a file or a CI log it is plain ASCII, because
escape codes in a log outlive the session that wrote them.

A scaffolded project calls it — the one place in that program that prints rather
than logs, because a build is a command somebody ran and is watching.

## Logs on a terminal

An application that configures no `Logger` gets one shaped for a person when its
output is a terminal:

```
23:06:09 • collage: listening  addr=localhost:6060
23:06:09 ▲ collage: no Security.CSRFKey set, so one was generated for this process  action=hello:POST
23:06:10 ✗ collage: request failed  path=/about stage=render
```

One line per record, a coloured marker instead of the level spelled out, the time
without the date — it is the same date as the terminal it is being read in — and the
attributes dimmed after the message.

Under `collage dev` the program's output is a pipe — `collage dev` reads it for the
error page on its way to the terminal — so on a colour terminal it runs the program
with `FORCE_COLOR=1`, and its own lines take the same shape. `FORCE_COLOR` counts
for a file or a pipe, never over `NO_COLOR`.

Anywhere that is not a terminal it is `slog.Default()`, unchanged, so nothing that
parses these logs has to learn a new format. And an application that called
`slog.SetDefault` keeps the handler it chose: noticing a terminal is not a reason to
override a decision somebody made on purpose. Setting `Config.Logger` settles it
either way — a JSON handler is what a program whose logs are read by a machine
should pass.

## What `collage new --template demo` gives you

A home page and a page of live demos, split into the directories a real project
grows into, with a test file that drives all of it through `app.Handler()` with
[`collagetest`](testing.md):

```
pages/<area>/<name>.go            a page: its layouts, content, path, actions
fragments/layouts/main.go         Master(), the layout every page wraps itself in
fragments/pages/<area>/<name>.go  each page's content, mirroring pages/
actions/<area>.go                 action builders, one file per area
actions/funcs/<area>.go           their handlers
documents/<name>.go               routes that are not HTML
data/<domain>/                    state, by domain
templates/                        the HTML kept in files
```

`pages/` and `fragments/pages/` mirror each other by area, and every package in
them is called `pages` or `fragments`, so a page file imports its content as
`fragments "<module>/fragments/pages/<area>"` and `routes.go` imports each area's
pages under an alias, `demopages`. Small fragments keep their markup inline, as a
`collage.InlineHTML` const beside their data handler; the layout and the larger
pages keep theirs in `templates/`. An action a page attaches with `WithActionFor`
shares the page's name and has no path of its own.

- **`/`** is `Static()`: nothing about it depends on the request, which is what
  lets `collage export` render it to a file.
- **`/features`** has four demos: a button posting to `/api/count`, an action
  that answers with JSON and invalidates the cached page by tag; a plain HTML form
  posting to `/hello`, whose own action renders the greeting; a clock fragment
  opened at `/fragments/clock` with `WithFragmentPath`; and `/healthz`, a JSON
  document. It is `Static()` too — cached, and invalidated by the count action —
  but it carries forms, so a static export skips it and says why.

Templates and static files are embedded, in `embed.go`, so the binary runs from
any working directory; development builds leave them out and read the directories
on disk, so editing a template is visible on the next request. Static files are linked with `{{asset}}`,
so they are served content-addressed and `immutable`.

It ships a `.env.example` with `COLLAGE_CSRF_KEY`, `PORT` and `HOST`; copy it to
`.env.development` for `collage dev`. Set `COLLAGE_CSRF_KEY` before deploying
anything with a form in it. Without one a key is generated per process, and the
application says so at startup — as a warning outside development, and only when
it has an action that could verify a token.


## `collage add`

Writes a page, a fragment, an action or a document in that layout, and registers
it in `routes.go`:

```
collage add page blog/post          # pages/blog/post.go + fragments/pages/blog/post.go
collage add page blog/post --file   # its template in templates/pages/blog/post.html
collage add fragment blog/sidebar   # fragments/pages/blog/sidebar.go, for a slot
collage add action blog/comment     # actions/blog.go + actions/funcs/blog.go
collage add action blog/ping --path /api/ping
collage add document feed --path /feed.xml --type application/xml
```

- **A page** is `pages/<area>/<name>.go`, wrapped in `layouts.Master()` — or the
  first layout in `fragments/layouts` that takes no arguments — with its content in
  `fragments/pages/<area>/<name>.go`: an inline template, a view struct, and a
  typed `collage.Load` handler that declares the page's title. Its path is
  `/<area>/<name>` unless `--path` says otherwise.
- **An action** gets a builder appended to `actions/<area>.go` and a handler to
  `actions/funcs/<area>.go`, either created when it is not there. With `--path` it
  answers at a URL of its own and is registered; without one it answers at the
  page it is attached to, and the command prints the `.WithActionFor(...)` to add.
- **A document** is `documents/<name>.go`, `text/plain` unless `--type` says
  otherwise.
- **A fragment** is a page's content without the page, for a slot.

What it writes is named after the last segment — `blog/post` is the page `"post"`,
built by `Post()` — unless `--name` gives another. The locale of its path is
`Locale.Default` as `main.go` writes it, `en` when it is not a literal there, or
`--locale`; a template file goes under `Template.Root` with its `Extension`, read
the same way. The command says which locale it used.

It registers into the list `routes.go` already keeps: an `app.Register(...)`
call, where the new item goes after the last one of its kind, or a
`[]*collage.Page{...}` literal ranged over with `RegisterPage` — and the same for
`Document` and `Action`. It edits the file in place, so its comments stay where
they were, and imports the area's package under the same kind of alias the
scaffold uses. With no such list it changes nothing there and prints the line to
add. Constructors take no arguments: a page that needs a service gets it added by
hand.

**Nothing is overwritten.** Everything is worked out before anything is written,
and a file that exists, a page, action or document name the project already
declares, or an identifier already in the package a file goes into stops the
command with nothing written.

## `collage new`

Scaffolds the runnable project described above: a `go.mod`, a `main.go` with the
configuration and the static mount, a `routes.go` registering every route, the
pages, fragments, action, document and their templates, the tests, a
`.env.example`, a `.gitignore`, and a README.

That is `--template demo`. The default, `--template minimal`, is the least a
project can be: the same `main.go`, `go.mod` and `routes.go`, and a layout around
one page, `<h1>Hello from {{.Name}}</h1>` — the project's name, handed to the
template by `WithData` — with a stylesheet that sets the background and text
colour, dark mode included — no tests, no not-found page, nothing to
delete before starting a real site. Flags take one dash or two.

The scaffolded `main.go` mounts `static/` with `os.OpenRoot`, **not** `os.DirFS`.
That is not a style preference: `os.DirFS` does not prevent symlink traversal, so
a symlink planted inside the mounted directory escapes it, while an `os.Root` is
enforced by the kernel. See [assets.md](assets.md).

```
collage new myblog                       # into ./myblog, module "myblog", one page
collage new myblog --template demo       # the demos, their tests, a README
collage new myblog -module github.com/me/myblog
collage new myblog -dir . -force         # into a non-empty directory
```

Flags may appear before or after the project name; the command splits the
positional argument out before parsing, so ordering does not matter.

## `collage dev` and `collage export`

Neither command builds your application itself — it cannot. `internal/cli` has no
way to import `pkg/collage` and construct your `App` in process, so it shells out
to `go` in the current directory, exactly as you would by hand:

| Command | Runs | With |
| --- | --- | --- |
| `collage dev` | `go build -tags collage_dev`, then the binary it built — again on every change | `COLLAGE_DEV=1`, and the `HOST` and `PORT` to listen on, in the environment |
| `collage export` | `go run . -collage-build -out <dir>` | `-clean` appended when you passed it |

That is a **contract with your `main.go`**, and the scaffolded one honours both
halves: it turns on development mode when `COLLAGE_DEV=1` is set, listens on the
`HOST` and `PORT` it is given, and it renders
to static files when `-collage-build` is passed instead of starting a server. If
you rewrite `main.go`, keep both halves working or these two commands stop doing
anything useful in your project.

### Rebuilding on change

Templates and static files are read from disk on every request in development, so
editing them needs nothing. A change to Go code is rebuilt and restarted, with no
tool to install:

- **What is watched** is what the program is made of: `.go` files (test files
  aside), `go.mod` and `go.sum` at the root, and the environment file. Hidden
  directories, `bin`, `dist`, `node_modules`, `testdata` and `vendor` are never
  looked at — so nothing the running program writes, its cache or an export, can
  set off a rebuild.
- **The new build is made first.** Only once it compiles is the old process
  stopped — interrupted, so it drains like it would on Ctrl-C — and the new one
  started. A change that does not compile leaves the last good build serving, with
  the compiler's error on screen.
- **Development builds embed nothing.** They are built with `-tags collage_dev`,
  and the scaffold keeps its `//go:embed` lines in `embed.go`, constrained with
  `//go:build !collage_dev`; `embed_dev.go` declares the same variables, empty, for
  the development build. Every build that embeds files stores another copy of them
  in the Go build cache — a change anywhere recompiles `main`, so that is one copy
  per save, kept for days: a 50 MB `static/` and a few hundred saves fill gigabytes.
  Development mode reads both directories from disk anyway, so there is nothing to
  embed. `collage build`, `collage export` and a plain `go build` pass no tag and
  embed as before. A project scaffolded before this keeps the lines in `main.go`;
  `collage dev` names the file when it starts, and moving them into the two files
  above fixes it.
- **A burst of writes is one rebuild.** A save that touches several files, or a
  formatter that rewrites one, is waited out before building.
- It polls rather than subscribing to file-system events, which keeps collage free
  of dependencies and works the same on every platform. A project is small enough
  that looking every 300 ms costs nothing noticeable.
- A program that exits by itself — a panic at startup, a page whose template is
  missing — is not restarted in a loop; the next change is what starts it again.
- **Errors are shown in the browser.** The browser talks to `collage dev`, not to
  the program: it listens on `HOST` and `PORT` as your program would read them
  (`localhost:6060` by default), and passes each request on to the program, which
  it starts with `HOST` and `PORT` set to a loopback address of its own. A request
  made while the program starts waits for it. When there is no program — it
  exited, or the first build failed — the page is a 503 showing what the program or
  the compiler printed, and it reloads by itself once a change brings the program
  back. A page already open when the program exits reloads onto that error.
- **The browser reloads too.** A development page reloads itself when a template or
  a static file changes, and when the program comes back from a rebuild — see
  [fragments.md](fragments.md#development-mode). Nothing to install in the browser.

### Environment files

`collage dev` adds the variables of `.env.development` to the environment, or of
`.env` when there is no `.env.development`. One file, never both — merging is a
second rule to explain for little gain.

- A variable already set in the shell wins, so `PORT=4000 collage dev` still works.
  `COLLAGE_DEV=1` is always set, whatever the file says, and so are the `HOST` and
  `PORT` the program is to listen on — the file's `HOST` and `PORT` are where
  `collage dev` itself listens, read once when it starts.
- `KEY=value` lines, `#` comments and blank lines; an `export ` prefix and single or
  double quotes around a value are allowed. A `#` after whitespace ends an unquoted
  value.
- A malformed line stops the command with the file and line number. A skipped line
  would be a setting you wrote and the program never saw.
- No file is not an error. The file that was read is named on stderr.

Only `collage dev` reads these files. `collage build`, `collage export` and the
built binary never do: production takes its environment from wherever it runs.

## `collage inspect`

Prints what the current directory's project is made of, as JSON — every page with
its patterns and parameters, every fragment with its template and slots, documents,
actions, the template functions, the plugins, the locales, and the files the mounts
serve. It runs `go run . collage-inspect`, which `DispatchCommands` answers itself
with `App.Inspect`, so a scaffolded project needs nothing more. It is what an
editor's completion reads: the Collage Snippets & Highlighter extension for VS Code
offers page names in `{{pageURL "…"}}`, slots in `{{slot "…"}}` and files in
`{{asset "…"}}` from it.

Each fragment carries the Go type its template sees as `.`, and the root carries
a `types` table describing the types those reach, so an editor can complete
`{{.` and check a field name the way registration does (see
[how templates are checked](fragments.md#how-templates-are-checked)):

```json
"fragments": [
  {"name": "post-body", "template": "pages/post.html", "handler": true, "dataType": "*blog.Post"},
  {"name": "layout", "template": "layouts/default.html", "dataType": "nil"},
  {"name": "legacy", "template": "pages/legacy.html", "handler": true, "dataType": null, "typeCheck": false}
],
"types": {
  "blog.Post": {
    "kind": "struct",
    "fields": [
      {"name": "Base", "type": "blog.Base", "embedded": true},
      {"name": "ID", "type": "int"},
      {"name": "Title", "type": "string"},
      {"name": "Author", "type": "*blog.User"},
      {"name": "Comments", "type": "blog.Comments"},
      {"name": "Related", "type": "blog.Tags"}
    ],
    "methods": [{"name": "URL", "args": 0, "returns": "string"}]
  },
  "blog.Base": {"kind": "struct", "fields": [{"name": "ID", "type": "int"}]},
  "blog.Comments": {"kind": "slice", "elem": "blog.Comment"},
  "blog.Tags": {"kind": "map", "key": "string", "elem": "[]blog.Post"},
  "blog.User": {"kind": "struct", "fields": [{"name": "Name", "type": "string"}]}
}
```

- `dataType` is always written: the Go type, `"nil"` for a fragment with no data
  or with `collage.Effect`, and `null` when the type is an interface and so
  unknown until the page renders — a handler declared to return `any`.
- `typeCheck: false` appears only on a fragment built `WithoutTypeCheck()`.
- `types` holds every named type reachable from some fragment's data type —
  through fields, element and key types, and method results — keyed by its Go
  name, with its exported fields (promoted ones included) and the exported
  methods of it and its pointer that return something. An exported embedded
  field is a field too — a template reaches it as `{{.Base}}` — and carries
  `"embedded": true`; the fields it promotes are listed beside it as usual. A
  named pointer, slice, array, map or chan carries `elem`, the type it holds,
  and a map also `key`. A type is listed once and named everywhere else, so a
  recursive type ends. An unnamed struct is listed
  under its Go spelling; other unnamed composites are written inline in a type
  string (`[]blog.Comment`); standard library types such as
  `time.Time` and `template.HTML` are named, not described. It is left out when no
  fragment has a known data type.
- A key two different types share (`models.User` from two packages named
  `models`) is written as `{"kind": "struct", "ambiguous": true}` with no fields
  or methods, and an editor treats it as unknown.

These are additions: the output's `version` is still `1`.

## `collage check`

Checks every template's links without rendering anything. A link built by name —
`{{pageURL "post" "slug" .Slug}}`, `{{pageURLIn "en" "about"}}`,
`{{actionURL "logout"}}`, `{{fragmentURL "home" "clock"}}`, `{{localeURL "en"}}` —
fails when the template renders, and only on the page that reaches it. `check`
finds them all at once:

```
$ collage check
error [unknown-route] inline template of fragment "hello":4:16: {{pageURL "featurs"}}: collage: no page or document by that name: "featurs"; did you mean "features"?
```

| Rule | What it means |
| --- | --- |
| `unknown-route` | No page, document, action or fragment path by that name; the closest registered name is suggested |
| `route-params` | The parameters do not fill the route's pattern — one missing, one it has no placeholder for, or not in name and value pairs |
| `unreachable-locale` | A locale no URL can carry: not `Locale.Default`, nor in `Locale.Supported` |
| `no-path-in-locale` | The route has no path in the locale `pageURLIn` or `fragmentURLIn` names |

It checks with the framework's own URL builders, so what it reports is exactly
what a render would fail on. Only names written as string literals are checked: a
name from a field, `{{pageURL .Name}}`, is known only when the template renders,
and a parameter whose name is not a literal leaves the parameters unchecked. A
link with no locale of its own passes when its route can be built in some locale,
since a render falls back to the default one.

It exits `1` when it finds anything, so it can stand in CI before `collage build`;
`-json` prints the findings as an array of `{level, rule, message}` for an editor.
It runs `go run . collage-check`, which `collage.DispatchCommands` answers by
starting the application and calling `App.Check` — the same function a test can
call. That needs the project's collage at v0.40.0 or later; an earlier one
answers `unknown command: "collage-check"`.

```go
if findings := app.Check(); len(findings) > 0 {
	t.Errorf("broken links: %v", findings)
}
```

## Plugin commands

A plugin registers a subcommand from its `Init`, through `Host.RegisterCommand`.

**The `collage` binary does not run them.** It never loads your application —
`dev` builds your project with `go build` and runs the result, `build` compiles
it, `export` runs `go run . -collage-build` — so it has no way to reach a command
that only exists once your plugins have been initialised. Your own `main`
dispatches them, with `collage.DispatchCommands`, and you run them as
`go run . <command>`. A scaffolded `main.go` already does:

```go
func main() {
	flag.Parse() // the program's own flags, whatever they are

	app, err := collage.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	registerPages(app)
	app.RegisterPlugin(sitemap.New())

	// Starts the application (running every plugin's Init, which is what
	// registers their commands) and dispatches the first word after the flags
	// against them. flag.Args(), not os.Args[1:]: the program's own flags are
	// not command names.
	code, err := collage.DispatchCommands(context.Background(), app, flag.Args())
	if !errors.Is(err, collage.ErrUnknownCommand) {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(code)
	}

	// Nothing claimed it: carry on and serve.
	log.Fatal(app.ListenAndServe())
}
```

`ErrUnknownCommand` is the fall-through signal — no arguments, or a name no plugin
claimed — so the program carries on with whatever it does by default. A command
that ran and failed returns exit code `1` with its own error wrapped; a nil `*App`
or an unclaimed name returns `2`. `DispatchCommands` has no built-ins of its own:
`new`, `dev`, `build`, `export`, `serve`, `version` and `help` belong to the
`collage` binary, which invokes your program rather than the other way round, and `App.Commands()` gives you the list if you want to
print one.

Dispatching starts the application, which closes registration — so call it after
everything is registered. A later `ListenAndServe` reuses that start rather than
repeating it.

Within the application, a command with an empty name or a name another command
already holds is rejected at `RegisterCommand` (`collage.ErrEmptyCommandName`,
`collage.ErrDuplicateCommand`). See [plugins](plugins.md).

## The static builder

The `-collage-build` half of the contract is `collage.NewBuilder` — the same
builder the CLI documentation refers to, reached through the public API. It
renders pages through `App.RenderPath` and documents through
`App.RenderDocumentPath`: the same render engine the HTTP server uses, the same
template set, the same fragment tree, the same data handlers, the same document
handlers.

**A static build runs your plugins, as the server does.** It does not render
through `App.Handler()` (only its header capture asks it, after the files are
written — see [static hosts](#static-hosts)), but `RenderPath` and `RenderDocumentPath` start the application
first — memoised, exactly as `Handler` does — so plugin `Init` has run before the
first page renders. Every page gets `OnBeforeRender` and `OnAfterRender`, and every
document `OnDocumentRendered`, so a minifier that shapes the served site shapes the
built one too. Two hooks do not fire, because they are about something a build is
not: `OnPageResolved` (a build is not a request) and `OnCacheWrite` (a build writes
files, not cache entries). See [plugins](plugins.md#static-builds).

A page that renders with a failed fragment is **not written**: the failure is
recorded in `report.Errors` as `collage.ErrDegradedRender`, because a static file
has no TTL to recover through and would keep serving that failure until the next
build. Set `BuildOptions.AllowDegraded` if a partial page is genuinely better than
no page. A page that renders no markup at all is always refused, as
`collage.ErrEmptyRender` — that used to reach disk as a zero-byte `index.html` and
an exit status of zero. A panic while building one page is recovered as
`collage.ErrBuildPanic` and the rest of the build continues.

```go
func staticBuild(app *collage.App, outDir string, clean bool) error {
	builder, err := collage.NewBuilder(app, collage.BuildOptions{
		OutDir: outDir,
		Clean:  clean,
	})
	if err != nil {
		return err
	}

	// report is populated even when err is not nil: every individual failure is
	// recorded in report.Errors and joined into the returned error.
	report, err := builder.Build(context.Background())
	for _, path := range report.Written {
		fmt.Println("wrote", path)
	}
	for _, skip := range report.Skipped {
		fmt.Printf("skip %s (%s): %s\n", skip.Page, skip.Locale, skip.Reason)
	}
	return err
}
```

### Options

| Field | Meaning |
| --- | --- |
| `OutDir` | Where output is written. Required |
| `Locales` | Restrict the build to these locales. Empty builds every locale a page declares |
| `Clean` | Remove `OutDir`'s contents (not `OutDir` itself) first |
| `Concurrency` | How many pages render and write at once. `<= 1` is sequential |

`Report` contents are deterministic regardless of `Concurrency`: tasks are merged
back into enumeration order.

### What gets built

- A page whose strategy is `Static()` or `Incremental(ttl)` is built, and so is one
  that declares none and renders no data handler. A dynamic page is recorded in
  `Report.Skipped` — it exists to render per request.
- Each page is written to `<OutDir>/<path>/index.html`; the root path `/` writes
  `<OutDir>/index.html`.
- A page whose pattern for a locale contains `{param}` needs `WithStaticParams`,
  or it is skipped with `ErrDynamicPathUnresolved`; see
  [below](#pages-with-a-param-in-their-path). It is a skip, not a failure: a
  build is not wrong for containing pages that cannot be prerendered.
- **A document is written to its literal path.** `/sitemap.xml` becomes
  `<OutDir>/sitemap.xml`, not `<OutDir>/sitemap.xml/index.html`, because a crawler
  asking for `/sitemap.xml` must not receive a directory. The same strategy rule
  applies — a `Dynamic()` document is skipped — and so does the `{param}`
  rule. A document handler that
  returns an empty body is refused with `collage.ErrEmptyDocumentBody` and no file
  is written, the same condition a live request answers with a 500.
- **Every mounted asset file system is copied**, under the prefix it is mounted
  at: `/static/app.css` becomes `<OutDir>/static/app.css`. Copying is the default;
  `collage.WithoutBuildCopy()` on a mount turns it off, for a mount served from a
  CDN in production or one too large to duplicate. Which mounts are copied comes
  from the application's own `Mounts()` — there is no `BuildOptions` field for it,
  so a caller cannot pair one application's pages with another's assets.
- **A locale other than the default is written under its prefix**, the URL the
  router serves it at: a page with `WithPath("en", "/about")` and
  `WithPath("tr", "/hakkinda")` writes `<OutDir>/about/index.html` and `<OutDir>/tr/hakkinda/index.html`. Pages
  and documents alike, so one pattern in two locales is two files — a `"tr"`
  document at `/feed.xml` is `<OutDir>/tr/feed.xml`. Do not write the prefix into
  the pattern yourself: the router strips it before matching, and `"/tr/blog"`
  would be served, and written, at `/tr/tr/blog`.

### Pages with a `{param}` in their path

A page at `/blog/{slug}` is one file per post, and the page says which posts with
`WithStaticParams` — one map of placeholder values per file, per locale:

```go
collage.NewPage("post").
	WithLayouts(layout).
	WithContent(post).
	WithPath("en", "/blog/{slug}").
	WithPath("tr", "/yazi/{slug}").
	Static().
	WithStaticParams(func(ctx context.Context, locale string) ([]map[string]string, error) {
		posts, err := store.List(ctx, locale)
		if err != nil {
			return nil, err
		}
		params := make([]map[string]string, 0, len(posts))
		for _, post := range posts {
			params = append(params, map[string]string{"slug": post.Slug})
		}
		return params, nil
	}).
	Build()
```

- The build makes each path from the pattern, as a link built by name would, and
  writes it under the locale's prefix: `<OutDir>/blog/hello/index.html`,
  `<OutDir>/tr/yazi/merhaba/index.html`. A value is written at its decoded path,
  which is where a static host looks a request for it up.
- The values reach the page's data handlers through `rc.Param`, exactly as a
  request to that path would carry them.
- A map that does not fill the pattern exactly — a name missing, or one the
  pattern does not have — fails that one file with `collage.ErrRouteParams`, and
  the rest are built. An error from the function, or a panic in it, fails that
  page's locale and is named in `Report.Errors`.
- Only a build calls it. A running server answers every value the pattern matches,
  listed or not.
- A page with a data handler is dynamic unless it says otherwise, so a post page
  that is to be exported says `Static()` or `Incremental(ttl)`.

Documents take the same `WithStaticParams`: a feed at `/feeds/{category}/rss.xml`
lists its categories.

### Static hosts

After the files are written, the build asks `App.Handler()` for each one's path —
twice, in-process — and records the status and headers it answers with on the
`BuiltFile` that `BuildFinishedHook` receives, leaving out the ones about a
single response and any that differ between the two answers. The hook's event
also carries every redirect the site declares, in `Redirects`: pages', documents'
and `collage.RedirectSource` plugins'. Two redirects from one path fail the
build (`collage.ErrDuplicateRedirect`), and so does one from a path the build
wrote (`collage.ErrRedirectShadowsFile`). Middleware sees the capture requests;
one that counts traffic should skip them with `collage.IsCapture`. Collage
writes no host files itself — `elagoht/deploy` writes them from the hook. See
[deployment](deployment.md#static-hosts).

### Safety

`WithStaticParams` and a mount's `fs.FS` are both your code, and a path built
from an unsanitised value — or a file name an
adversarial `fs.FS` yields from a walk — must not be able to write outside
`OutDir`. Pages, documents and copied asset files all go through the same checks
rather than three copies of them. The builder:

- refuses to run at all when `OutDir` resolves — after following symlinks — to a
  filesystem root, and refuses to `Clean` (though it still writes) when it
  resolves to a repository root (`ErrDangerousOutDir`);
- checks every resolved output path for containment with `filepath.Rel`, not a
  string prefix, so `/outsibling` cannot pass a check meant to require containment
  in `/out` (`ErrPathEscapesOutDir`);
- walks every path component from `OutDir` down to the file itself *before*
  creating any directory, and rejects the write if a component is a symlink that
  resolves outside `OutDir`. A symlink is not rejected for being one — a shared
  assets directory symlinked into the output is legitimate — only for leaving.

**The symlink check is best-effort, not race-free.** Nothing stops another process
from replacing a component with a symlink between the check and the `MkdirAll` and
`WriteFile` that follow it; closing that window portably is not possible with the
standard library alone. What the check does close is the planted-symlink case — a
symlink left in `OutDir` in advance by a buggy `WithStaticParams`, a misbehaving
plugin, or a stale artifact from an earlier build. A local attacker racing the
build process is a different threat, and a much less relevant one for a builder a
developer runs on their own machine.

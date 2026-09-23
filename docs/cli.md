# The CLI and static builds

```
go install github.com/Elagoht/collage/cmd/collage@latest
```

```
collage new <name> [-dir path] [-module path] [-force]
collage dev
collage build [-out dir] [-clean]
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
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w"
```

CGO off, because collage and the standard library need no C and a static binary is
what can go into an image holding nothing else. `-trimpath`, so the binary does not
carry the paths of the machine that built it. `-s -w`, which is most of the size.

**The default target is linux/amd64, not this machine.** A binary built on a Mac
does not run in a Linux container, and `exec format error` on a server is the wrong
place to find that out. `-os` and `-arch` change it; `-o` changes where it lands.

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
- **`404.html` is served with a 404** when the export has one.
- **Nothing is cached.** The point is to look at what was just exported, and a
  browser holding the previous one is what stops that.

Port 4000 rather than 3000, so this and a project under `collage dev` can be up at
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

▲ 1 page skipped
    signup  page uses the dynamic render strategy, which cannot be built statically

3 written · 1 skipped · 0 failed · 2.4ms
```

Written files are summarised — ten of them, then a count, because a build that wrote
three hundred must not bury the one it skipped. Skips and failures are never
truncated: a skip is the build telling you a page is not in its output, and that is
what a build is for saying. The last line carries every count and is coloured by the
worst of them, so it answers "how did that go" without being read.

Colour and the ✓ ▲ ✗ markers appear only on a terminal, and not when `NO_COLOR` is
set or `TERM` is `dumb`. Piped to a file or a CI log it is plain ASCII, because
escape codes in a log outlive the session that wrote them.

A scaffolded project calls it, and so does `examples/magazine` — the one place in
that program that prints rather than logs, because a build is a command somebody ran
and is watching.

## Logs on a terminal

An application that configures no `Logger` gets one shaped for a person when its
output is a terminal:

```
23:06:09 • collage: listening  addr=127.0.0.1:3000
23:06:09 ▲ collage: no Security.CSRFKey set, so one was generated for this process  action=signup:POST
23:06:10 ✗ collage: request failed  path=/about stage=render
```

One line per record, a coloured marker instead of the level spelled out, the time
without the date — it is the same date as the terminal it is being read in — and the
attributes dimmed after the message.

Anywhere that is not a terminal it is `slog.Default()`, unchanged, so nothing that
parses these logs has to learn a new format. And an application that called
`slog.SetDefault` keeps the handler it chose: noticing a terminal is not a reason to
override a decision somebody made on purpose. Setting `Config.Logger` settles it
either way — `examples/magazine` passes a JSON handler, which is what a program whose
logs are read by a machine should do.

## What `collage new` gives you

Two pages, and the difference between them is the lesson:

- **`/`** is `Static()`. Its data handler returns values from Go rather than from
  the template, so the wiring is visible, and nothing about it depends on the
  request — which is what lets `collage export` render it to a file.
- **`/signup`** is `Dynamic()`, and carries a form: a `{{csrfToken}}`, a validation
  failure that re-renders the page with 422, and a success that redirects with 303.
  A form needs a server to post to, so a static build skips this page and says so.

Templates and static files are embedded, so the binary runs from any working
directory; development mode still prefers the directory on disk, so editing a
template is visible on the next request. The stylesheet is linked with `{{asset}}`,
so it is served content-addressed and `immutable`. `PORT` and `-port` move the
server off 3000.

Set `COLLAGE_CSRF_KEY` before deploying anything with a form in it. Without one a
key is generated per process, and the application says so at startup — but only when
it has an action that could verify a token, because telling an application with no
forms about a key it has no use for is how a warning becomes noise.


## `collage new`

Scaffolds a runnable project: a `go.mod`, a `main.go` wiring one page and mounting
one static directory, a layout and a home template, a starter stylesheet under
`static/`, a `.gitignore`, and a README.

The scaffolded `main.go` mounts `static/` with `os.OpenRoot`, **not** `os.DirFS`.
That is not a style preference: `os.DirFS` does not prevent symlink traversal, so
a symlink planted inside the mounted directory escapes it, while an `os.Root` is
enforced by the kernel. See [assets.md](assets.md).

```
collage new myblog                       # into ./myblog, module "myblog"
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
| `collage dev` | `go run .` | `COLLAGE_DEV=1` in the environment |
| `collage export` | `go run . -collage-build -out <dir>` | `-clean` appended when you passed it |

That is a **contract with your `main.go`**, and the scaffolded one honours both
halves: it turns on development mode when `COLLAGE_DEV=1` is set, and it renders
to static files when `-collage-build` is passed instead of starting a server. If
you rewrite `main.go`, keep both halves working or these two commands stop doing
anything useful in your project.

`collage dev` reloads *templates* from disk on every request. It does not
hot-reload Go code: a change to a `.go` file still needs a restart.

## Plugin commands

A plugin registers a subcommand from its `Init`, through `Host.RegisterCommand`.

**The `collage` binary does not run them.** It never loads your application — its
`dev` and `build` commands shell out to `go run .` in your project directory — so
it has no way to reach a command that only exists once your plugins have been
initialised. Your own `main` dispatches them, with `collage.DispatchCommands`:

```go
func main() {
	app, err := collage.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	registerPages(app)
	app.RegisterPlugin(sitemap.New())

	// Starts the application (running every plugin's Init, which is what
	// registers their commands) and dispatches args[0] against them.
	code, err := collage.DispatchCommands(context.Background(), app, os.Args[1:])
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
`dev` and `build` belong to the `collage` binary, which invokes your program rather
than the other way round, and `App.Commands()` gives you the list if you want to
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

**A static build renders without plugins — pages and documents alike.** It does
not go through `App.Handler()`, so plugin `Init` never runs and no hook fires:
nothing an `OnPageResolved`, `OnBeforeRender`, `OnAfterRender`, or `OnCacheWrite`
would have contributed appears in the files it writes. A plugin that stamps every
page from `OnAfterRender` stamps nothing here. What you get is the fragment output
a live request would produce, and the bytes a document handler returned, without
whatever the plugin layer adds on top of them.

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
| `PathProvider` | Supplies the concrete paths for a page whose pattern has a `{param}` |
| `DocumentPathProvider` | The same, for a document whose pattern has a `{param}`. A separate interface, not a widening of `PathProvider`, so an existing implementation keeps compiling |

`Report` contents are deterministic regardless of `Concurrency`: tasks are merged
back into enumeration order.

### What gets built

- A page whose strategy is `Static()` or `Incremental(ttl)` is built. A `Dynamic()`
  page is recorded in `Report.Skipped` — it exists to render per request.
- Each page is written to `<OutDir>/<path>/index.html`; the root path `/` writes
  `<OutDir>/index.html`.
- A page whose pattern for a locale contains `{param}` needs a `PathProvider`, or
  it is skipped with `ErrDynamicPathUnresolved`. It is a skip, not a failure: a
  build is not wrong for containing pages that cannot be prerendered.
- **A document is written to its literal path.** `/sitemap.xml` becomes
  `<OutDir>/sitemap.xml`, not `<OutDir>/sitemap.xml/index.html`, because a crawler
  asking for `/sitemap.xml` must not receive a directory. The same strategy rule
  applies — a `Dynamic()` document is skipped — and a dynamic pattern needs a
  `DocumentPathProvider` rather than a `PathProvider`. A document handler that
  returns an empty body is refused with `collage.ErrEmptyDocumentBody` and no file
  is written, the same condition a live request answers with a 500.
- **Every mounted asset file system is copied**, under the prefix it is mounted
  at: `/static/app.css` becomes `<OutDir>/static/app.css`. Copying is the default;
  `collage.WithoutBuildCopy()` on a mount turns it off, for a mount served from a
  CDN in production or one too large to duplicate. Which mounts are copied comes
  from the application's own `Mounts()` — there is no `BuildOptions` field for it,
  so a caller cannot pair one application's pages with another's assets.
- The output path comes from the page's pattern for that locale, with no locale
  prefix added. Two locales sharing one pattern therefore write to the same file —
  give each locale its own path (`"/blog/{slug}"` and `"/tr/blog/{slug}"`) if you
  build more than one.

### Supplying paths for dynamic pages

```go
// postPaths expands "/blog/{slug}" into one path per post.
type postPaths struct {
	store *PostStore
}

// Paths implements collage.PathProvider.
func (p postPaths) Paths(ctx context.Context, page *collage.Page, locale string) ([]collage.PathInstance, error) {
	pattern, ok := page.PathFor(locale)
	if !ok || !strings.Contains(pattern, "{slug}") {
		return nil, nil
	}

	posts := p.store.List()
	instances := make([]collage.PathInstance, 0, len(posts))
	for _, post := range posts {
		instances = append(instances, collage.PathInstance{
			Path:   strings.Replace(pattern, "{slug}", post.Slug, 1),
			Params: map[string]string{"slug": post.Slug},
		})
	}
	return instances, nil
}
```

`Params` matters: it is overlaid onto whatever the router captured, so a data
handler sees the same parameters a live request would have.

### Safety

A `PathProvider`, a `DocumentPathProvider`, and a mount's `fs.FS` are all your
code, and a path built from an unsanitised parameter — or a file name an
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
symlink left in `OutDir` in advance by a buggy `PathProvider`, a misbehaving
plugin, or a stale artifact from an earlier build. A local attacker racing the
build process is a different threat, and a much less relevant one for a builder a
developer runs on their own machine.

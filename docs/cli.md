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

## `collage new`

Scaffolds a runnable project: a `go.mod`, a `main.go` wiring one page, a layout
and a home template, a `.gitignore`, and a README.

```
collage new myblog                       # into ./myblog, module "myblog"
collage new myblog -module github.com/me/myblog
collage new myblog -dir . -force         # into a non-empty directory
```

Flags may appear before or after the project name; the command splits the
positional argument out before parsing, so ordering does not matter.

## `collage dev` and `collage build`

Neither command builds your application itself — it cannot. `internal/cli` has no
way to import `pkg/collage` and construct your `App` in process, so it shells out
to `go` in the current directory, exactly as you would by hand:

| Command | Runs | With |
| --- | --- | --- |
| `collage dev` | `go run .` | `COLLAGE_DEV=1` in the environment |
| `collage build` | `go run . -collage-build -out <dir>` | `-clean` appended when you passed it |

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
renders through `App.RenderPath`: the same render engine the HTTP server uses, the
same template set, the same fragment tree, the same data handlers.

**A static build renders without plugins.** It does not go through `App.Handler()`,
so plugin `Init` never runs and no render hook fires: nothing an
`OnPageResolved`, `OnBeforeRender`, `OnAfterRender`, or `OnCacheWrite` would have
contributed appears in the files it writes. A plugin that stamps every page from
`OnAfterRender` stamps nothing here. What you get is the fragment output a live
request would produce, without whatever the plugin layer adds on top of it.

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

A `PathProvider` is your code, and a path built from an unsanitised parameter must
not be able to write outside `OutDir`. The builder therefore:

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

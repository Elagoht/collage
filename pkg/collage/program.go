package collage

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"
)

// Env is what Main knows about the run before the application exists: what
// "collage dev" and the environment asked for. Main hands it to the function
// that builds the application; a test builds one by hand.
type Env struct {
	// Dev is development mode: COLLAGE_DEV=1, which "collage dev" sets.
	Dev bool
	// Host and Port are where to listen: HOST, or "localhost"; -port, PORT, or
	// 6060. "collage dev" sets both.
	Host string
	Port int
	// CacheDir is the directory a disk cache keeps rendered pages in: ".cache".
	// A test points it at a directory of its own.
	CacheDir string
}

// PluginConfig returns plugins-config.json, keyed by plugin name: the file in
// the working directory when there is one — so a deployment can change a
// setting without a rebuild — and otherwise the copy files embeds, so a binary
// deployed without the file still has its settings. In development it is only
// ever the file on disk. A file in neither place is no error; every plugin
// then runs on its defaults.
func (e Env) PluginConfig(files fs.FS) (map[string]json.RawMessage, error) {
	if !e.Dev && files != nil {
		if _, err := os.Stat(pluginConfigFile); errors.Is(err, fs.ErrNotExist) {
			if raw, err := fs.ReadFile(files, pluginConfigFile); err == nil {
				return parsePluginConfig(pluginConfigFile, raw)
			}
		}
	}
	return LoadPluginConfig(pluginConfigFile)
}

// pluginConfigFile is the plugin configuration's file, which "collage plugin
// add" writes.
const pluginConfigFile = "plugins-config.json"

// Static returns the directory dir of files, for App.Mount: in development
// the directory on disk, so an edited stylesheet shows up without a rebuild,
// and the embedded copy otherwise, or when the disk has none.
//
// The disk is opened with os.OpenRoot, which, unlike os.DirFS, does not follow
// a symlink out of the directory.
func (e Env) Static(files fs.FS, dir string) fs.FS {
	if e.Dev {
		if root, err := os.OpenRoot(filepath.FromSlash(dir)); err == nil {
			return root.FS()
		}
	}
	if files == nil {
		return emptyFS{}
	}
	sub, err := fs.Sub(files, path.Clean(dir))
	if err != nil {
		return errFS{err}
	}
	return sub
}

// Main runs the program "collage new" scaffolds, around newApp, which builds
// the application from env. It keeps the contract the collage binary relies
// on:
//
//   - with no arguments it serves the application until SIGINT or SIGTERM;
//   - with -collage-build, which "collage export" passes with -out and
//     -clean, it renders every static page to files;
//   - with a word after the flags it runs that command — a plugin's,
//     "collage-inspect" or "collage-check" — and exits with its code.
//
// A build or a command shuts the application down before Main returns, so
// every plugin's Shutdown runs. Main exits the process on failure, and after a
// command; a test calls newApp itself rather than Main.
func Main(newApp func(Env) (*App, error)) {
	os.Exit(runMain(newApp, os.Args[1:], os.Stdout, os.Stderr))
}

// runMain is Main without the exit, for its tests.
func runMain(newApp func(Env) (*App, error), args []string, stdout, stderr io.Writer) int {
	name := filepath.Base(os.Args[0])
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	build := flags.Bool("collage-build", false, "render the app to static files instead of serving it")
	out := flags.String("out", "dist", "output directory for -collage-build")
	clean := flags.Bool("clean", false, "remove -out's existing contents before building")
	port := flags.Int("port", envInt("PORT", 6060), "port to listen on (env PORT)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	host := os.Getenv("HOST")
	if host == "" {
		host = "localhost"
	}
	env := Env{Dev: os.Getenv("COLLAGE_DEV") == "1", Host: host, Port: *port, CacheDir: ".cache"}

	app, err := newApp(env)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}

	if rest := flags.Args(); len(rest) > 0 {
		code, err := DispatchCommands(context.Background(), app, rest)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
		}
		if err := shutdownStarted(app); err != nil {
			fmt.Fprintf(stderr, "%s: shutdown: %v\n", name, err)
			code = max(code, 1)
		}
		return code
	}

	if *build {
		report, err := Build(context.Background(), app, BuildOptions{OutDir: *out, Clean: *clean})
		PrintBuildReport(stdout, report, err)
		err = errors.Join(err, shutdownStarted(app))
		if err != nil {
			fmt.Fprintf(stderr, "%s: static build: %v\n", name, err)
			return 1
		}
		return 0
	}

	if err := app.ListenAndServe(); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	return 0
}

// shutdownStarted runs every plugin's Shutdown once a build or a command is
// done; neither serves, so nothing else would. Only after a successful start:
// one that failed has shut down what it started, and Shutdown runs once.
func shutdownStarted(app *App) error {
	if app.Start() != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return app.Shutdown(ctx)
}

// envInt is the environment variable key as a number, or fallback. A value
// that is not one falls back: a port is not worth refusing to start over.
func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}

// emptyFS is Static's directory when there are no files to take it from.
type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// errFS is Static's directory when dir cannot be one: every open fails with
// why.
type errFS struct{ err error }

func (f errFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: f.err}
}

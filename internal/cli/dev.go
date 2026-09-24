package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// CommandRunner runs an external command. dev and build go through this
// interface, rather than calling os/exec directly, so a test can substitute a
// fake and assert the command that would have run — its directory,
// environment, name, and arguments — without starting a real process or a
// real server.
type CommandRunner interface {
	// Run starts name with args in dir (the empty string means the calling
	// process's own current directory), with env appended to the started
	// process's environment, streaming its output to stdout and stderr, and
	// blocks until it exits.
	Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error
}

// devUsage is "collage help dev"'s own usage text.
const devUsage = `Usage: collage dev

Builds the Go project in the current directory, runs it with COLLAGE_DEV=1
set, and rebuilds and restarts it whenever its Go code changes.

The scaffolded main.go (see "collage new") reads COLLAGE_DEV and turns on
development mode, which reads templates and static files from disk on every
request — so editing those needs no rebuild, and none happens.

What is watched is what the program is made of: .go files (test files aside),
go.mod and go.sum, and the environment file below. Hidden directories, bin,
dist, node_modules, testdata and vendor are not looked at, so nothing the
running program writes — its cache, an export — can trigger a rebuild. The new
build is made before the old process is stopped: a change that does not
compile leaves the last good build serving, and says why.

Variables are read from .env.development in the current directory, or from
.env when there is no .env.development — one file, never both. It holds
KEY=value lines, "#" comments and blank lines; an "export " prefix and quotes
around a value are allowed. A variable already set in the shell wins over the
file, so "PORT=4000 collage dev" still works, and COLLAGE_DEV=1 is always set.
The file read is named on stderr when there is one; no file is not an error.

Only "collage dev" reads these files. "collage build", "collage export" and the
built binary take their environment from wherever they run.
`

// runDev implements the "dev" command.
func (c *CLI) runDev(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	fs.SetOutput(c.stderr())
	fs.Usage = func() { fmt.Fprint(c.stderr(), devUsage) }

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(c.stderr(), "collage: dev takes no arguments")
		fmt.Fprintln(c.stderr())
		fs.Usage()
		return 2
	}

	// Ctrl-C reaches the running program too, which shuts itself down; this is
	// what stops the loop around it from exiting before it has.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Builds go outside the project, so the watcher never sees them and a
	// half-written binary is never in a directory anybody serves.
	buildDir, err := os.MkdirTemp("", "collage-dev-")
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: dev: %v\n", err)
		return 1
	}
	defer os.RemoveAll(buildDir)

	(&devLoop{cli: c, buildDir: buildDir}).run(ctx)
	return 0
}

// devLoop is one "collage dev" session: the program it is running, and how to
// build the next one.
type devLoop struct {
	cli      *CLI
	buildDir string
	builds   int
	current  *devProcess
	// loaded is the environment file the last restart read, so it is named
	// when it changes rather than on every rebuild.
	loaded string
}

// devProcess is one running build of the program.
type devProcess struct {
	binary string
	stop   context.CancelFunc
	done   chan error
}

// run builds and starts the program, then rebuilds and restarts it on every
// change until ctx is done.
func (l *devLoop) run(ctx context.Context) {
	defer l.stopCurrent()

	snapshot, _ := snapshotSources(".")
	l.restart(ctx)

	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	for {
		var exited chan error
		if l.current != nil {
			exited = l.current.done
		}

		select {
		case <-ctx.Done():
			return

		case err := <-exited:
			// Its done channel is drained, so it is no longer current whatever
			// happens next: stopCurrent waiting on it again would wait forever.
			os.Remove(l.current.binary)
			l.current = nil
			// Stopped with the session: Ctrl-C reaches the program too, and it
			// can exit before this loop hears about the signal.
			if ctx.Err() != nil {
				return
			}
			// Stopped by itself: a crash, or a port already in use. Starting it
			// again would only repeat that, so the next change is what does.
			fmt.Fprintf(l.cli.stderr(), "collage: dev: the program exited (%v); waiting for a change\n", exitReason(err))

		case <-ticker.C:
			next, err := snapshotSources(".")
			if err != nil || next.equal(snapshot) {
				continue
			}
			// Editors and formatters write in bursts. Waiting for one quiet
			// interval means one rebuild per save rather than one per write.
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(watchInterval):
				}
				settled, err := snapshotSources(".")
				if err == nil && settled.equal(next) {
					break
				}
				next = settled
			}
			snapshot = next
			fmt.Fprintln(l.cli.stderr(), "collage: dev: change detected, rebuilding")
			l.restart(ctx)
		}
	}
}

// restart builds the program and, if the build succeeds, replaces the running
// one with it. A failed build leaves the running one alone.
func (l *devLoop) restart(ctx context.Context) {
	stderr := l.cli.stderr()

	// Read on every restart, so an edited environment file takes effect — it is
	// watched for exactly that reason.
	env, loaded, err := loadDevEnv(".", os.LookupEnv)
	if err != nil {
		fmt.Fprintf(stderr, "collage: dev: %v\n", err)
		return
	}
	if loaded != "" && loaded != l.loaded {
		fmt.Fprintf(stderr, "collage: dev: loaded %s\n", loaded)
	}
	l.loaded = loaded
	// Last, so no file can turn development mode off: a later duplicate wins
	// in the started process's environment.
	env = append(env, "COLLAGE_DEV=1")

	l.builds++
	binary := filepath.Join(l.buildDir, fmt.Sprintf("app-%d%s", l.builds, exeSuffix()))
	if err := l.cli.runner().Run(ctx, "", nil, l.cli.stdout(), stderr, "go", "build", "-o", binary, "."); err != nil {
		if ctx.Err() != nil {
			return
		}
		if l.current != nil {
			fmt.Fprintln(stderr, "collage: dev: build failed; the last good build is still serving")
		} else {
			fmt.Fprintln(stderr, "collage: dev: build failed; waiting for a change")
		}
		return
	}

	l.stopCurrent()

	processCtx, stop := context.WithCancel(ctx)
	process := &devProcess{binary: binary, stop: stop, done: make(chan error, 1)}
	go func() {
		process.done <- l.cli.runner().Run(processCtx, "", env, l.cli.stdout(), stderr, binary)
	}()
	l.current = process
}

// stopCurrent stops the running program, if there is one, and waits for it.
func (l *devLoop) stopCurrent() {
	if l.current == nil {
		return
	}
	l.current.stop()
	<-l.current.done
	os.Remove(l.current.binary)
	l.current = nil
}

// exitReason describes how a program exited on its own.
func exitReason(err error) string {
	if err == nil {
		return "status 0"
	}
	return err.Error()
}

// exeSuffix is the file extension an executable needs on this platform.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

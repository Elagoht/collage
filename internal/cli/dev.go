package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"go/build"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Elagoht/collage/internal/devhost"
	"github.com/Elagoht/collage/internal/term"
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

It builds with -tags collage_dev. The scaffolded project embeds templates/ and
static/ in a file constrained with "//go:build !collage_dev", so development
builds embed nothing: they read both from disk anyway, and every build that
embeds them stores another copy of them in the Go build cache — gigabytes, over
a few days of saves. A project whose main package still embeds files in a
development build is told so when "collage dev" starts.

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

The browser talks to "collage dev" itself, at HOST and PORT as the program
would read them (localhost:6060 by default), and each request is passed on to
the program, which is started with HOST and PORT set to a loopback address of
its own. A request made while the program is starting waits for it. When there
is no program — it exited, or the first build failed — the page is its output,
and it reloads by itself once a change brings the program back.
`

// devBuildTag is the build tag "collage dev" builds with. The scaffold keeps its
// //go:embed directives in a file constrained with "//go:build !collage_dev", so
// a development build embeds nothing.
const devBuildTag = "collage_dev"

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

	// The address is settled once, from the shell and the environment file as
	// they are now: moving it would move the page open in the browser.
	env, _, _ := loadDevEnv(".", os.LookupEnv)
	public := devAddress(env, os.LookupEnv)
	target, err := freeLoopbackAddress()
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: dev: %v\n", err)
		return 1
	}
	listener, err := net.Listen("tcp", public)
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: dev: %v\n", err)
		return 1
	}
	proxy := newDevProxy(public, target)
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	// Its own lines in the shape the program's are, so the two read as one log.
	log := slog.New(term.NewHandler(c.stderr(), slog.LevelInfo))
	style := term.NewStyle(c.stderr())
	log.Info("collage dev: serving " + style.Bold("http://"+public))

	(&devLoop{cli: c, log: log, color: style.Rich(), buildDir: buildDir, proxy: proxy}).run(ctx)
	return 0
}

// devLoop is one "collage dev" session: the program it is running, and how to
// build the next one.
type devLoop struct {
	cli *CLI
	log *slog.Logger
	// color is whether the terminal "collage dev" writes to takes colour, and
	// so whether the program is told to colour output it cannot see is going
	// there: its stderr is a pipe, read for the error page on the way.
	color    bool
	buildDir string
	builds   int
	current  *devProcess
	proxy    *devProxy
	// loaded is the environment file the last restart read, so it is named
	// when it changes rather than on every rebuild.
	loaded string
}

// devProcess is one running build of the program.
type devProcess struct {
	binary string
	stop   context.CancelFunc
	done   chan error
	// output is the end of what it printed, for the page shown if it exits.
	output *tailBuffer
}

// run builds and starts the program, then rebuilds and restarts it on every
// change until ctx is done.
func (l *devLoop) run(ctx context.Context) {
	defer l.stopCurrent()

	l.warnEmbedded()
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
			exitedProcess := l.current
			exitedProcess.stop()
			os.Remove(exitedProcess.binary)
			l.current = nil
			// Stopped with the session: Ctrl-C reaches the program too, and it
			// can exit before this loop hears about the signal.
			if ctx.Err() != nil {
				return
			}
			// Stopped by itself: a crash, or a port already in use. Starting it
			// again would only repeat that, so the next change is what does.
			l.log.Warn("collage dev: the program exited; waiting for a change", "reason", exitReason(err))
			l.proxy.down(exitedProcess.output.String() + fmt.Sprintf("collage: dev: the program exited (%v); waiting for a change\n", exitReason(err)))

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
			l.log.Info("collage dev: change detected, rebuilding")
			l.restart(ctx)
		}
	}
}

// warnEmbedded warns when the main package embeds files in a development build.
// The compiled package holds what it embeds, and every rebuild — which is every
// save, since a change anywhere recompiles main — stores another copy of it in
// the Go build cache, kept there for days. A package it cannot read is left to
// the build to complain about.
func (l *devLoop) warnEmbedded() {
	ctx := build.Default
	ctx.BuildTags = append(slices.Clone(ctx.BuildTags), devBuildTag)
	pkg, err := ctx.ImportDir(".", 0)
	if err != nil || len(pkg.EmbedPatterns) == 0 {
		return
	}
	var files []string
	for _, positions := range pkg.EmbedPatternPos {
		for _, position := range positions {
			files = append(files, filepath.Base(position.Filename))
		}
	}
	slices.Sort(files)
	l.log.Warn("collage dev: "+strings.Join(slices.Compact(files), ", ")+
		" embeds files into every development build, and each build stores another copy of them in the Go build cache;"+
		" move the //go:embed lines into a file constrained with //go:build !"+devBuildTag+" (see collage help dev)",
		"patterns", strings.Join(pkg.EmbedPatterns, " "))
}

// restart builds the program and, if the build succeeds, replaces the running
// one with it. A failed build leaves the running one alone.
func (l *devLoop) restart(ctx context.Context) {
	// Read on every restart, so an edited environment file takes effect — it is
	// watched for exactly that reason.
	// With nothing serving, what follows is what the browser waits for.
	if l.current == nil {
		l.proxy.starting(nil)
	}

	env, loaded, err := loadDevEnv(".", os.LookupEnv)
	if err != nil {
		l.log.Error("collage dev: " + err.Error())
		if l.current == nil {
			l.proxy.down(fmt.Sprintf("collage: dev: %v\n", err))
		}
		return
	}
	if loaded != "" && loaded != l.loaded {
		l.log.Info("collage dev: loaded " + loaded)
	}
	l.loaded = loaded
	// Last, so no file can turn development mode off, or move the program off
	// the address the proxy passes requests to: a later duplicate wins in the
	// started process's environment.
	host, port, _ := net.SplitHostPort(l.proxy.target)
	if l.color {
		env = append(env, "FORCE_COLOR=1")
	}
	// The proxy's own host, too: the program listens on loopback, but the
	// browser's Host — passed on unchanged — is the proxy's, and the program's
	// development Host check must allow what the proxy allowed.
	publicHost, _, err := net.SplitHostPort(l.proxy.public)
	if err != nil {
		publicHost = l.proxy.public
	}
	env = append(env, "HOST="+host, "PORT="+port, devhost.EnvHost+"="+publicHost, "COLLAGE_DEV=1")

	l.builds++
	binary := filepath.Join(l.buildDir, fmt.Sprintf("app-%d%s", l.builds, exeSuffix()))
	buildOutput := &tailBuffer{}
	stderr := l.cli.stderr()
	if err := l.cli.runner().Run(ctx, "", nil, l.cli.stdout(), io.MultiWriter(stderr, buildOutput), "go", "build", "-tags", devBuildTag, "-o", binary, "."); err != nil {
		if ctx.Err() != nil {
			return
		}
		if l.current != nil {
			l.log.Error("collage dev: build failed; the last good build is still serving")
		} else {
			l.log.Error("collage dev: build failed; waiting for a change")
			l.proxy.down(buildOutput.String() + "collage: dev: build failed; waiting for a change\n")
		}
		return
	}

	l.stopCurrent()

	processCtx, stop := context.WithCancel(ctx)
	process := &devProcess{binary: binary, stop: stop, done: make(chan error, 1), output: &tailBuffer{}}
	l.proxy.starting(process)
	go l.proxy.awaitListening(processCtx, process)
	// The program reports the address it listens on, which is the proxy's
	// target; the one to open is the proxy's own, so that is what it says.
	stdout := &addressRewriter{w: l.cli.stdout(), from: l.proxy.target, to: l.proxy.public}
	programErr := &addressRewriter{w: io.MultiWriter(stderr, process.output), from: l.proxy.target, to: l.proxy.public}
	go func() {
		err := l.cli.runner().Run(processCtx, "", env, stdout, programErr, binary)
		stdout.Flush()
		programErr.Flush()
		process.done <- err
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

// addressRewriter passes output on with every occurrence of from replaced by to.
//
// It holds back only the end of a write that could be the start of from, so an
// address split across two writes is still replaced, and output with no
// address in it is passed on as it comes. Flush writes what is held back.
type addressRewriter struct {
	mu      sync.Mutex
	w       io.Writer
	from    string
	to      string
	pending []byte
}

func (a *addressRewriter) Write(b []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	text := strings.ReplaceAll(string(a.pending)+string(b), a.from, a.to)
	a.pending = a.pending[:0]
	// The longest suffix that is a proper prefix of from.
	for keep := min(len(a.from)-1, len(text)); keep > 0; keep-- {
		if strings.HasPrefix(a.from, text[len(text)-keep:]) {
			a.pending = append(a.pending, text[len(text)-keep:]...)
			text = text[:len(text)-keep]
			break
		}
	}
	if _, err := io.WriteString(a.w, text); err != nil {
		return 0, err
	}
	return len(b), nil
}

// Flush writes whatever Write held back.
func (a *addressRewriter) Flush() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.pending) > 0 {
		a.w.Write(a.pending)
		a.pending = a.pending[:0]
	}
}

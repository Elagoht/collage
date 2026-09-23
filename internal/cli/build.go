package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Elagoht/collage/internal/term"
)

// buildUsage is "collage help build"'s own usage text.
const buildUsage = `Usage: collage build [-o path] [-os name] [-arch name] [-i]

Compiles the current directory's project into the binary you deploy.

A collage project is a Go program, and what you put on a server is a compiled
one — not the source, not the toolchain. This runs the go build somebody would
otherwise have to remember:

  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w"

CGO off because collage and the standard library need no C, and a static binary
is what can go into an image with nothing else in it. -trimpath so the binary
does not carry the paths of the machine that built it. -s -w to drop the debug
tables, which is most of the size.

The default target is linux/amd64 rather than this machine: a binary built for
a Mac does not run in a Linux container, and finding that out from "exec format
error" on a server is the wrong place to learn it.

  -o path     where to write the binary (default "bin/<module name>")
  -os name    target operating system (default "linux")
  -arch name  target architecture (default "amd64")
  -i          ask which extra files to write beside it — a Dockerfile, a
              systemd unit, written into the same directory as the binary.
              Without it, only the binary is written.

To render the project to static files instead, see "collage export".
`

// runBuild implements the "build" command.
func (c *CLI) runBuild(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(c.stderr())
	fs.Usage = func() { fmt.Fprint(c.stderr(), buildUsage) }
	out := fs.String("o", "", `where to write the binary (default "bin/<module name>")`)
	goos := fs.String("os", "linux", "target operating system")
	goarch := fs.String("arch", "amd64", "target architecture")
	interactive := fs.Bool("i", false, "ask which extra files to write beside the binary")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(c.stderr(), "collage: build takes no positional arguments")
		fmt.Fprintln(c.stderr())
		fs.Usage()
		return 2
	}

	name, err := moduleName()
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: build: %v\n", err)
		return 1
	}
	target := *out
	if target == "" {
		// bin/, not dist/. dist/ is where "collage export" writes a static
		// site, and it removes that directory's contents with -clean — which
		// would delete a binary sitting in it. Two commands, two outputs, two
		// directories.
		target = filepath.Join("bin", name)
	}
	if *goos == "windows" && !strings.HasSuffix(target, ".exe") {
		target += ".exe"
	}

	extras := extraFiles{}
	if *interactive {
		extras = c.askForExtras(name, filepath.Dir(target), *goos, *goarch)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		fmt.Fprintf(c.stderr(), "collage: build: %v\n", err)
		return 1
	}

	started := time.Now()
	env := []string{
		"CGO_ENABLED=0",
		"GOOS=" + *goos,
		"GOARCH=" + *goarch,
	}
	buildArgs := []string{"build", "-trimpath", "-ldflags=-s -w", "-o", target, "."}

	if err := c.runner().Run(ctx, "", env, c.stdout(), c.stderr(), "go", buildArgs...); err != nil {
		fmt.Fprintf(c.stderr(), "collage: build: %v\n", err)
		return 1
	}
	elapsed := time.Since(started)

	written, err := extras.write(name, filepath.Dir(target))
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: build: %v\n", err)
		return 1
	}

	c.reportBuild(target, *goos, *goarch, elapsed, written)
	return 0
}

// reportBuild prints what was produced, in the same shape a static export's report
// takes: what is there, then one line saying how it went.
func (c *CLI) reportBuild(target, goos, goarch string, elapsed time.Duration, extras []string) {
	w := c.stdout()
	s := term.NewStyle(w)

	size := "unknown size"
	if info, err := os.Stat(target); err == nil {
		size = humanBytes(info.Size())
	}
	platform := goos + "/" + goarch

	fmt.Fprintf(w, "\n%s %s\n", s.OK(s.Mark("✓", "+")), s.Bold(target))
	fmt.Fprintf(w, "    %s\n", s.Dim(platform+" · "+size))
	for _, path := range extras {
		line := "wrote " + path
		if filepath.Base(path) == "Dockerfile" {
			// The one thing moving it out of the project root costs, said at
			// the moment somebody would otherwise have to work it out.
			line += "    docker build -f " + path + " ."
		}
		fmt.Fprintf(w, "    %s\n", s.Dim(line))
	}
	fmt.Fprintf(w, "\n%s\n", s.OK(fmt.Sprintf("%s · %s · %s", size, platform, elapsed.Round(100*time.Millisecond))))
}

// humanBytes renders a size the way a person reads one.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGT"[exp])
}

// moduleName reads the module path from go.mod and returns its last element, which
// is what the binary is called.
//
// Reading the file rather than running "go list": this has to work before anything
// is built, and a project whose dependencies are not downloaded yet should still be
// able to be told what is wrong rather than fail inside the toolchain.
func moduleName() (string, error) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return "", fmt.Errorf("no go.mod here: %w", err)
	}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "module "))
		if path == "" {
			break
		}
		return filepath.Base(path), nil
	}
	return "", errors.New("go.mod declares no module path")
}

// extraFiles is what to write beside the binary.
type extraFiles struct {
	dockerfile bool
	systemd    bool
}

// write produces the chosen files beside the binary, in dir, and returns the paths
// of the ones it created.
//
// Beside the binary rather than at the project root, because they are generated and
// the root is for what a person wrote. The cost is one flag at the other end —
// "docker build -f bin/Dockerfile ." — which the report prints so nobody has to work
// it out.
//
// An existing file is never overwritten, and that is not a convenience: these are
// files a project edits, and a build command that replaces one with a default is a
// build command that quietly undoes somebody's work.
func (e extraFiles) write(name, dir string) ([]string, error) {
	var written []string

	if e.dockerfile {
		path, err := writeIfAbsent(filepath.Join(dir, "Dockerfile"), dockerfileFor(name))
		if err != nil {
			return written, err
		}
		if path != "" {
			written = append(written, path)
		}
	}
	if e.systemd {
		path, err := writeIfAbsent(filepath.Join(dir, name+".service"), systemdUnitFor(name))
		if err != nil {
			return written, err
		}
		if path != "" {
			written = append(written, path)
		}
	}
	return written, nil
}

// writeIfAbsent writes content to path unless something is already there, and
// reports the path it wrote or the empty string.
func writeIfAbsent(path, content string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		return "", nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// askForExtras prompts for the files to write beside the binary.
func (c *CLI) askForExtras(name, dir, goos, goarch string) extraFiles {
	out := c.stdout()
	fmt.Fprintf(out, "Building %s for %s/%s.\n\n", name, goos, goarch)

	reader := bufio.NewReader(c.stdinOrDefault())
	ask := func(question, existing string) bool {
		if _, err := os.Stat(existing); err == nil {
			fmt.Fprintf(out, "  %s already exists — leaving it alone.\n", existing)
			return false
		}
		fmt.Fprintf(out, "  %s [y/N]: ", question)
		answer, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		return answer == "y" || answer == "yes"
	}

	extras := extraFiles{
		dockerfile: ask("Write a Dockerfile?", filepath.Join(dir, "Dockerfile")),
		systemd:    ask("Write a systemd unit?", filepath.Join(dir, name+".service")),
	}
	fmt.Fprintln(out)
	return extras
}

// stdinOrDefault returns the CLI's input, or the process's.
func (c *CLI) stdinOrDefault() io.Reader {
	if c.Stdin != nil {
		return c.Stdin
	}
	return os.Stdin
}

// dockerfileFor is the two-stage image a scaffolded project deploys as.
func dockerfileFor(name string) string {
	return `# Built by "collage build -i".
#
# Two stages, and the second one holds the binary and nothing else: a collage
# project embeds its templates and its static files, so there is nothing beside
# the binary to copy. CGO is off, which is what makes the binary static enough
# for a distroless base.
FROM golang:` + goVersion() + ` AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /` + name + ` .

FROM gcr.io/distroless/static-debian12
# WORKDIR matters: the rendered-page cache is a path, so it lands wherever the
# process was started. Everything else about this project travels in the binary.
WORKDIR /srv
COPY --from=build /` + name + ` /usr/local/bin/` + name + `
ENV HOST=0.0.0.0 PORT=8080
# Set this to at least 32 random bytes, kept with your other secrets. Without
# it a key is generated per process, and every form submitted before a restart
# is refused after it.
# ENV COLLAGE_CSRF_KEY=
EXPOSE 8080
ENTRYPOINT ["` + "/usr/local/bin/" + name + `"]
`
}

// systemdUnitFor is a unit for a project deployed without a container.
func systemdUnitFor(name string) string {
	return `# Written by "collage build -i". Install it at
# /etc/systemd/system/` + name + `.service, then:
#
#   systemctl daemon-reload && systemctl enable --now ` + name + `
#
# Adjust User, WorkingDirectory and the binary's path first — this file cannot
# know them.
[Unit]
Description=` + name + `
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=` + name + `
# The rendered-page cache is written relative to this directory, and the user
# above has to be able to write here.
WorkingDirectory=/srv/` + name + `
ExecStart=/usr/local/bin/` + name + `
Environment=HOST=127.0.0.1
Environment=PORT=8080
# At least 32 random bytes. Without it a key is generated per process, so every
# form submitted before a restart is refused after it.
# Environment=COLLAGE_CSRF_KEY=
Restart=on-failure
RestartSec=2

# collage traps SIGTERM and drains in-flight requests, so the default KillSignal
# is the right one; this only has to be longer than Server.ShutdownTimeout.
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
`
}

// goVersion is the toolchain tag a generated Dockerfile pins, taken from the one
// that built this CLI so the image is not built with something older.
//
// Major and minor only. "golang:1.26" keeps picking up patch releases, which is
// what a base image should do; pinning the patch this CLI happened to be built
// with would freeze an image on a Go release for as long as nobody regenerates
// the file.
func goVersion() string {
	version := strings.TrimPrefix(runtime.Version(), "go")
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return version
	}
	return parts[0] + "." + parts[1]
}

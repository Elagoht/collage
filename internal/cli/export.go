package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// exportUsage is "collage help export"'s own usage text.
const exportUsage = `Usage: collage export [-out dir] [-clean]

Renders the current directory's project to static files: HTML for every page
that can be one, plus every mounted asset. What you deploy to a static host.

For a server — a project with forms, or pages that render per request — see
"collage build", which produces the binary instead.

Runs "go run . -collage-build -out <dir>" in the current directory's Go
project, appending -clean when this command's own -clean was given.

The scaffolded main.go (see "collage new") parses -collage-build, -out, and
-clean and, on seeing -collage-build, renders the project to static files
under <dir> instead of starting a server — removing <dir>'s existing contents
first when -clean was given. It prints what it wrote as it writes it; that
output is streamed straight through, not reformatted by this command.

  -out dir   directory the project renders static files into (default "dist")
  -clean     remove -out's existing contents before building
`

// runExport implements the "export" command.
func (c *CLI) runExport(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(c.stderr())
	fs.Usage = func() { fmt.Fprint(c.stderr(), exportUsage) }
	out := fs.String("out", "dist", "directory the project renders static files into")
	clean := fs.Bool("clean", false, "remove -out's existing contents before building")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(c.stderr(), "collage: export takes no positional arguments")
		fmt.Fprintln(c.stderr())
		fs.Usage()
		return 2
	}

	runArgs := []string{"run", ".", "-collage-build", "-out", *out}
	if *clean {
		runArgs = append(runArgs, "-clean")
	}

	err := c.runner().Run(ctx, "", nil, c.stdout(), c.stderr(), "go", runArgs...)
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: export: %v\n", err)
		return 1
	}
	return 0
}

// stopGrace is how long a cancelled child process has to exit after it is
// interrupted before it is killed.
const stopGrace = 10 * time.Second

// execRunner is the CommandRunner used outside tests: it runs the command as a
// real child process via os/exec, connecting its stdio to the given writers.
type execRunner struct{}

// Run implements CommandRunner by starting a real child process and waiting
// for it to exit.
//
// A cancelled ctx interrupts the process rather than killing it, so a served
// application shuts down the way it would on Ctrl-C — draining its requests —
// and is killed only if it has not exited within stopGrace.
func (execRunner) Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			// Windows cannot deliver an interrupt to another process.
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = stopGrace
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd.Run()
}

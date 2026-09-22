package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// buildUsage is "collage help build"'s own usage text.
const buildUsage = `Usage: collage build [-out dir]

Runs "go run . -collage-build -out <dir>" in the current directory's Go
project.

The scaffolded main.go (see "collage new") parses -collage-build and -out and,
on seeing them, renders the project to static files under <dir> instead of
starting a server. It prints what it wrote as it writes it; that output is
streamed straight through, not reformatted by this command.

  -out dir   directory the project renders static files into (default "dist")
`

// runBuild implements the "build" command.
func (c *CLI) runBuild(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(c.stderr())
	fs.Usage = func() { fmt.Fprint(c.stderr(), buildUsage) }
	out := fs.String("out", "dist", "directory the project renders static files into")

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

	err := c.runner().Run(ctx, "", nil, c.stdout(), c.stderr(), "go", "run", ".", "-collage-build", "-out", *out)
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: build: %v\n", err)
		return 1
	}
	return 0
}

// execRunner is the CommandRunner used outside tests: it runs the command as a
// real child process via os/exec, connecting its stdio to the given writers.
type execRunner struct{}

// Run implements CommandRunner by starting a real child process and waiting
// for it to exit.
func (execRunner) Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd.Run()
}

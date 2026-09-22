package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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

Runs "go run ." in the current directory's Go project, with COLLAGE_DEV=1 set
in the process environment.

The scaffolded main.go (see "collage new") reads COLLAGE_DEV and turns on
development mode, which reloads templates from disk on every request. It does
NOT hot-reload Go code: a change to this project's own .go files still
requires stopping and re-running "collage dev".
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

	err := c.runner().Run(ctx, "", []string{"COLLAGE_DEV=1"}, c.stdout(), c.stderr(), "go", "run", ".")
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: dev: %v\n", err)
		return 1
	}
	return 0
}

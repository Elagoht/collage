package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/Elagoht/collage/internal/clienv"
)

// runDevProgram builds the current directory's project the way "collage dev"
// does — with the collage_dev tag, into a file of its own — and runs it with
// args and COLLAGE_DEV=1. inspect, routes and check ask the program about
// itself this way rather than with "go run .": go run stores the linked
// executable in the Go build cache, and without the tag the compiled main
// package holds every file the scaffold embeds, so each run after a change to
// main stored another copy of templates/ and static/ there, kept for days.
// Built without them, the program reads them from disk, as development mode
// does.
//
// The build's own output goes to stderr, so stdout carries only what the
// program prints.
func (c *CLI) runDevProgram(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	dir, err := os.MkdirTemp("", "collage-run-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// Named after the project, as go run names it, so the program's messages
	// still start with the project's name.
	name := "app"
	if wd, err := os.Getwd(); err == nil {
		name = filepath.Base(wd)
	}
	binary := filepath.Join(dir, name+exeSuffix())
	if err := c.runner().Run(ctx, "", nil, stderr, stderr, "go", "build", "-tags", devBuildTag, "-o", binary, "."); err != nil {
		return err
	}
	// The word asked with, too: a program that never hands it to
	// DispatchCommands starts serving, and collage refuses to while it is set,
	// rather than leave this run waiting on a server.
	return c.runner().Run(ctx, "", []string{"COLLAGE_DEV=1", clienv.Command + "=" + args[0]}, stdout, stderr, binary, args...)
}

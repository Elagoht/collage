// Command collage is the collage CLI: it scaffolds new projects
// (collage new) and drives an existing project's dev and static-build
// workflows (collage dev, collage build).
//
// main is deliberately thin: it constructs a cli.CLI, runs it, and exits with
// the code it returns. This is the only place os.Exit appears anywhere in
// this program — every other function returns a value instead, so the whole
// CLI is testable without a subprocess.
package main

import (
	"context"
	"os"

	"github.com/Elagoht/collage/internal/cli"
)

func main() {
	c := &cli.CLI{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	os.Exit(c.Run(context.Background(), os.Args[1:]))
}

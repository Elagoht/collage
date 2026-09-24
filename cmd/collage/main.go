// Command collage is the collage CLI: it scaffolds new projects
// (collage new), drives an existing project's development loop (collage dev),
// compiles it into the binary you deploy (collage build), renders it to static
// files (collage export), and serves such an export the way a static host would
// (collage serve).
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

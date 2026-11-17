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
	"os/signal"

	"github.com/Elagoht/collage/internal/cli"
)

func main() {
	// One context for the whole run, cancelled by Ctrl-C, a terminate or a
	// hangup. With a bare Background context an interrupt killed the process
	// where it stood, so no command's deferred cleanup ran: the temporary
	// directories inspect, routes and check build into were left behind. Now
	// the command returns, cleaning up, and the exit below follows.
	ctx, stop := signal.NotifyContext(context.Background(), cli.ShutdownSignals...)
	code := (&cli.CLI{Stdout: os.Stdout, Stderr: os.Stderr}).Run(ctx, os.Args[1:])
	// os.Exit skips defers, so the handler is released explicitly.
	stop()
	os.Exit(code)
}

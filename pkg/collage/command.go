package collage

import (
	"context"
	"errors"
	"fmt"
)

// ErrNilApp is returned by DispatchCommands when passed a nil *App. There is nothing
// to dispatch against and nothing to start, and inventing an application would run a
// program the caller never built.
var ErrNilApp = errors.New("collage: nil app")

// ErrUnknownCommand is returned by DispatchCommands when args is empty or names no
// command any registered plugin contributed. It is the signal to fall through to
// whatever the program does by default — serving, building, or printing its own
// usage — so test for it with errors.Is rather than treating every non-nil error as
// fatal.
var ErrUnknownCommand = errors.New("collage: unknown command")

// DispatchCommands runs the plugin-contributed CLI subcommand named by args[0], with
// args[1:] as its arguments, and returns the exit code the program should use
// alongside the error that produced it.
//
// It exists because a plugin's commands are otherwise unreachable. A plugin registers
// one from inside Init, through Host.RegisterCommand; Init runs when the application
// starts; and the `collage` binary never loads the application at all — its dev and
// build commands shell out to "go run ." in the project directory. Nothing was ever
// going to dispatch those commands but the application's own main, and until this
// function there was no way for it to.
//
// DispatchCommands starts the application first (see App.Start), because Init is what
// registers the commands: there is nothing to dispatch against before it has run. A
// startup failure is returned with exit code 1 and nothing is dispatched. Starting
// also closes registration, so call it after everything is registered — which is the
// same rule Handler and ListenAndServe already impose, and a later ListenAndServe on
// the same App reuses this start rather than repeating it.
//
// It dispatches only plugin commands. It has no built-ins of its own: "dev" and
// "build" belong to the `collage` binary, which invokes this program rather than the
// other way round, and a program that wants a usage listing has App.Commands.
//
// The exit codes follow the `collage` binary's: 0 for success, 2 for a usage problem
// (a nil App, or a name nothing claims), and 1 for a command that ran and failed.
//
//	code, err := collage.DispatchCommands(ctx, app, os.Args[1:])
//	if !errors.Is(err, collage.ErrUnknownCommand) {
//		if err != nil {
//			fmt.Fprintln(os.Stderr, err)
//		}
//		os.Exit(code)
//	}
//	// no plugin claimed it: carry on and serve
//	log.Fatal(app.ListenAndServe())
func DispatchCommands(ctx context.Context, app *App, args []string) (int, error) {
	if app == nil {
		return 2, ErrNilApp
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := app.Start(); err != nil {
		return 1, err
	}
	if len(args) == 0 {
		return 2, ErrUnknownCommand
	}

	name, rest := args[0], args[1:]
	for _, cmd := range app.Commands() {
		if cmd.Name != name {
			continue
		}
		if cmd.Run == nil {
			// A registered command with no Run is the plugin's mistake, not the
			// user's: reporting it as unknown would send them looking at their
			// command line instead of at the plugin.
			return 1, fmt.Errorf("collage: command %q has no Run function", name)
		}
		if err := cmd.Run(ctx, rest); err != nil {
			return 1, fmt.Errorf("collage: %s: %w", name, err)
		}
		return 0, nil
	}

	return 2, fmt.Errorf("%w: %q", ErrUnknownCommand, name)
}

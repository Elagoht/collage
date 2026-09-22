package collage

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// commandPlugin registers one CLI command from Init — the only place a plugin can —
// and records what that command was run with.
type commandPlugin struct {
	name string
	err  error

	ran  bool
	args []string
}

var _ Plugin = (*commandPlugin)(nil)

// Name identifies the plugin.
func (p *commandPlugin) Name() string { return "commander" }

// Version reports the plugin's version.
func (p *commandPlugin) Version() string { return "1.0.0" }

// Init registers the plugin's command with the host.
func (p *commandPlugin) Init(_ context.Context, host Host) error {
	return host.RegisterCommand(Command{
		Name:  p.name,
		Usage: p.name + " [flags]",
		Short: "a command contributed by a plugin",
		Run: func(_ context.Context, args []string) error {
			p.ran = true
			p.args = args
			return p.err
		},
	})
}

// Shutdown does nothing.
func (p *commandPlugin) Shutdown(context.Context) error { return nil }

// TestDispatchCommands_RunsAPluginCommand is the I6 regression. A plugin registers a
// command from Init, Init runs when the application starts, and the `collage` binary
// never loads the application — so before this function nothing in the framework
// could ever dispatch one, however the documentation described it.
func TestDispatchCommands_RunsAPluginCommand(t *testing.T) {
	app := registrationTestApp(t)
	p := &commandPlugin{name: "sitemap"}
	if err := app.RegisterPlugin(p); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	code, err := DispatchCommands(context.Background(), app, []string{"sitemap", "--out", "public"})

	if err != nil {
		t.Fatalf("DispatchCommands: %v", err)
	}
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
	if !p.ran {
		t.Fatal("the plugin's command never ran")
	}
	if len(p.args) != 2 || p.args[0] != "--out" || p.args[1] != "public" {
		t.Errorf("args = %v, want [--out public]: the command name must not be passed through", p.args)
	}
}

// TestDispatchCommands_UnknownIsDistinguishable checks the fall-through contract: a
// name nothing claims, and an empty argument list, both report ErrUnknownCommand so a
// program can carry on and serve rather than exiting.
func TestDispatchCommands_UnknownIsDistinguishable(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "no arguments", args: nil},
		{name: "a name nothing claims", args: []string{"nope"}},
		{name: "a built-in of the collage binary", args: []string{"build"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := registrationTestApp(t)
			if err := app.RegisterPlugin(&commandPlugin{name: "sitemap"}); err != nil {
				t.Fatalf("RegisterPlugin: %v", err)
			}

			code, err := DispatchCommands(context.Background(), app, c.args)

			if !errors.Is(err, ErrUnknownCommand) {
				t.Fatalf("err = %v, want ErrUnknownCommand", err)
			}
			if code != 2 {
				t.Errorf("code = %d, want 2", code)
			}
		})
	}
}

// TestDispatchCommands_FailingCommandIsExitCodeOne separates "you typed something
// nobody knows" from "the command ran and failed", which get different exit codes and
// which a caller must not confuse.
func TestDispatchCommands_FailingCommandIsExitCodeOne(t *testing.T) {
	app := registrationTestApp(t)
	boom := errors.New("sitemap generation failed")
	if err := app.RegisterPlugin(&commandPlugin{name: "sitemap", err: boom}); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	code, err := DispatchCommands(context.Background(), app, []string{"sitemap"})

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the command's own error", err)
	}
	if errors.Is(err, ErrUnknownCommand) {
		t.Error("a command that ran and failed must not report ErrUnknownCommand")
	}
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(err.Error(), "sitemap") {
		t.Errorf("err = %q, want it to name the command", err)
	}
}

// TestDispatchCommands_NilApp covers the one input that cannot be dispatched at all.
func TestDispatchCommands_NilApp(t *testing.T) {
	code, err := DispatchCommands(context.Background(), nil, []string{"sitemap"})
	if !errors.Is(err, ErrNilApp) {
		t.Fatalf("err = %v, want ErrNilApp", err)
	}
	if code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
}

// TestDispatchCommands_StartupFailureIsReported checks that a failed Init is surfaced
// rather than swallowed into "unknown command". Handler cannot report a startup
// failure — it has to return an http.Handler — which is why App.Start exists.
func TestDispatchCommands_StartupFailureIsReported(t *testing.T) {
	app := registrationTestApp(t)
	boom := errors.New("plugin init failed")
	if err := app.RegisterPlugin(&failingInitPlugin{err: boom}); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}

	code, err := DispatchCommands(context.Background(), app, []string{"sitemap"})

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the startup failure", err)
	}
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}

// failingInitPlugin fails its Init, aborting startup.
type failingInitPlugin struct {
	err error
}

var _ Plugin = (*failingInitPlugin)(nil)

// Name identifies the plugin.
func (p *failingInitPlugin) Name() string { return "failing-init" }

// Version reports the plugin's version.
func (p *failingInitPlugin) Version() string { return "1.0.0" }

// Init fails with the plugin's fixed error.
func (p *failingInitPlugin) Init(context.Context, Host) error { return p.err }

// Shutdown does nothing.
func (p *failingInitPlugin) Shutdown(context.Context) error { return nil }

// TestApp_Start_IsIdempotentAndReportsFailure pins App.Start's own contract, which
// DispatchCommands depends on: it runs the work once and every caller sees the same
// result, and it reports what Handler cannot.
func TestApp_Start_IsIdempotentAndReportsFailure(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		app := registrationTestApp(t)
		p := &commandPlugin{name: "sitemap"}
		if err := app.RegisterPlugin(p); err != nil {
			t.Fatalf("RegisterPlugin: %v", err)
		}

		if err := app.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := app.Start(); err != nil {
			t.Fatalf("second Start: %v", err)
		}
		if cmds := app.Commands(); len(cmds) != 1 || cmds[0].Name != "sitemap" {
			t.Fatalf("Commands() = %v, want exactly one \"sitemap\": Init ran once", cmds)
		}
		if err := app.RegisterPage(simplePage("late").Build()); !errors.Is(err, ErrAppStarted) {
			t.Errorf("RegisterPage after Start = %v, want ErrAppStarted", err)
		}
	})

	t.Run("failure", func(t *testing.T) {
		app := registrationTestApp(t)
		boom := errors.New("plugin init failed")
		if err := app.RegisterPlugin(&failingInitPlugin{err: boom}); err != nil {
			t.Fatalf("RegisterPlugin: %v", err)
		}

		first := app.Start()
		if !errors.Is(first, boom) {
			t.Fatalf("Start = %v, want the plugin's error", first)
		}
		if second := app.Start(); !errors.Is(second, boom) {
			t.Fatalf("second Start = %v, want the same memoised error", second)
		}
	})
}

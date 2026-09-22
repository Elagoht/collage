package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/plugin"
)

// testCLI returns a CLI with fresh, independent buffers for Stdout and
// Stderr, and no Runner (dev/build tests set one explicitly).
func testCLI() (*CLI, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return &CLI{Stdout: &out, Stderr: &errOut}, &out, &errOut
}

func TestRun_NoArgs(t *testing.T) {
	c, out, errOut := testCLI()

	code := c.Run(context.Background(), nil)

	if code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
	if !strings.Contains(errOut.String(), "no command given") {
		t.Errorf("stderr = %q, want it to mention the missing command", errOut.String())
	}
	if !strings.Contains(errOut.String(), "Usage:") {
		t.Errorf("stderr = %q, want usage text", errOut.String())
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	c, _, errOut := testCLI()

	code := c.Run(context.Background(), []string{"frobnicate"})

	if code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), `"frobnicate"`) {
		t.Errorf("stderr = %q, want it to name the unknown command", errOut.String())
	}
}

func TestRun_Version(t *testing.T) {
	c, out, errOut := testCLI()

	code := c.Run(context.Background(), []string{"version"})

	if code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
	want := "collage version " + Version + "\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
}

func TestRun_Help_NoArgs(t *testing.T) {
	c, out, errOut := testCLI()

	code := c.Run(context.Background(), []string{"help"})

	if code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
	for _, want := range []string{"new", "dev", "build", "version", "help", "Usage:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout = %q, want it to contain %q", out.String(), want)
		}
	}
}

func TestRun_Help_Command(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"new", "collage new <name>"},
		{"dev", "collage dev"},
		{"build", "collage build"},
		{"version", "collage version"},
		{"help", "collage help"},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			c, out, errOut := testCLI()

			code := c.Run(context.Background(), []string{"help", tt.command})

			if code != 0 {
				t.Fatalf("Run() = %d, want 0", code)
			}
			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want empty", errOut.String())
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Errorf("stdout = %q, want it to contain %q", out.String(), tt.want)
			}
		})
	}
}

func TestRun_Help_UnknownCommand(t *testing.T) {
	c, out, errOut := testCLI()

	code := c.Run(context.Background(), []string{"help", "frobnicate"})

	if code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
	if !strings.Contains(errOut.String(), `"frobnicate"`) {
		t.Errorf("stderr = %q, want it to name the unknown command", errOut.String())
	}
}

func TestRun_PluginCommand_DispatchedByName(t *testing.T) {
	c, out, _ := testCLI()

	var gotArgs []string
	c.Commands = []plugin.Command{
		{
			Name:  "greet",
			Usage: "greet <name>",
			Short: "Print a greeting",
			Run: func(_ context.Context, args []string) error {
				gotArgs = args
				out.WriteString("hello\n")
				return nil
			},
		},
	}

	code := c.Run(context.Background(), []string{"greet", "world"})

	if code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
	if out.String() != "hello\n" {
		t.Errorf("stdout = %q, want %q", out.String(), "hello\n")
	}
	if len(gotArgs) != 1 || gotArgs[0] != "world" {
		t.Errorf("plugin command args = %v, want [world]", gotArgs)
	}
}

func TestRun_PluginCommand_ErrorExitsOne(t *testing.T) {
	c, _, errOut := testCLI()

	wantErr := errors.New("boom")
	c.Commands = []plugin.Command{
		{Name: "explode", Run: func(context.Context, []string) error { return wantErr }},
	}

	code := c.Run(context.Background(), []string{"explode"})

	if code != 1 {
		t.Fatalf("Run() = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "boom") {
		t.Errorf("stderr = %q, want it to contain the command's error", errOut.String())
	}
}

func TestRun_PluginCommand_ListedInHelp(t *testing.T) {
	c, out, _ := testCLI()
	c.Commands = []plugin.Command{
		{Name: "greet", Short: "Print a greeting", Run: func(context.Context, []string) error { return nil }},
	}

	code := c.Run(context.Background(), []string{"help"})

	if code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "greet") || !strings.Contains(out.String(), "Print a greeting") {
		t.Errorf("stdout = %q, want it to list the plugin command", out.String())
	}
}

func TestRun_PluginCommand_CollidesWithBuiltin_Rejected(t *testing.T) {
	c, _, errOut := testCLI()
	c.Commands = []plugin.Command{
		{Name: "build", Run: func(context.Context, []string) error { return nil }},
	}

	// Even an unrelated invocation is refused: a malformed command table is a
	// configuration error, not something only the colliding name's own
	// invocation should trip over.
	code := c.Run(context.Background(), []string{"version"})

	if code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "collides with a built-in command") {
		t.Errorf("stderr = %q, want it to mention the collision", errOut.String())
	}
}

func TestRun_PluginCommand_DuplicateName_Rejected(t *testing.T) {
	c, _, errOut := testCLI()
	c.Commands = []plugin.Command{
		{Name: "greet", Run: func(context.Context, []string) error { return nil }},
		{Name: "greet", Run: func(context.Context, []string) error { return nil }},
	}

	code := c.Run(context.Background(), []string{"help"})

	if code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "duplicate command name") {
		t.Errorf("stderr = %q, want it to mention the duplicate", errOut.String())
	}
}

func TestRun_PluginCommand_EmptyName_Rejected(t *testing.T) {
	c, _, errOut := testCLI()
	c.Commands = []plugin.Command{
		{Name: "", Run: func(context.Context, []string) error { return nil }},
	}

	code := c.Run(context.Background(), []string{"help"})

	if code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "empty command name") {
		t.Errorf("stderr = %q, want it to mention the empty name", errOut.String())
	}
}

// fakeRunner is a CommandRunner that records the arguments of its last call
// instead of starting anything, so dev and build tests can assert the
// constructed command without spawning a real process.
type fakeRunner struct {
	dir       string
	env       []string
	name      string
	args      []string
	err       error
	callCount int
}

func (f *fakeRunner) Run(_ context.Context, dir string, env []string, _, _ io.Writer, name string, args ...string) error {
	f.dir = dir
	f.env = append([]string(nil), env...)
	f.name = name
	f.args = append([]string(nil), args...)
	f.callCount++
	return f.err
}

func TestRun_Dev_ConstructsExpectedCommand(t *testing.T) {
	c, _, _ := testCLI()
	runner := &fakeRunner{}
	c.Runner = runner

	code := c.Run(context.Background(), []string{"dev"})

	if code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
	if runner.callCount != 1 {
		t.Fatalf("runner called %d times, want 1", runner.callCount)
	}
	if runner.name != "go" {
		t.Errorf("name = %q, want %q", runner.name, "go")
	}
	if got, want := strings.Join(runner.args, " "), "run ."; got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
	if len(runner.env) != 1 || runner.env[0] != "COLLAGE_DEV=1" {
		t.Errorf("env = %v, want [COLLAGE_DEV=1]", runner.env)
	}
}

func TestRun_Dev_RunnerErrorExitsOne(t *testing.T) {
	c, _, errOut := testCLI()
	c.Runner = &fakeRunner{err: errors.New("exit status 1")}

	code := c.Run(context.Background(), []string{"dev"})

	if code != 1 {
		t.Fatalf("Run() = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "exit status 1") {
		t.Errorf("stderr = %q, want it to contain the runner's error", errOut.String())
	}
}

func TestRun_Dev_RejectsExtraArgs(t *testing.T) {
	c, _, errOut := testCLI()
	c.Runner = &fakeRunner{}

	code := c.Run(context.Background(), []string{"dev", "extra"})

	if code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}
	if errOut.Len() == 0 {
		t.Error("stderr is empty, want a usage message")
	}
}

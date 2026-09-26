// Package cli implements the collage command-line tool: scaffolding a new
// project, and driving an existing one's dev and static-build workflows.
//
// Every command writes to the io.Writers the CLI struct carries rather than to
// os.Stdout/os.Stderr directly, and Run returns an exit code instead of calling
// os.Exit itself, so the whole surface is testable without touching the real
// process streams or spawning a real process. cmd/collage/main.go is the only
// place os.Exit is called.
//
// dev, build and export operate on the current directory's own Go project —
// the project that imports pkg/collage — rather than on anything internal/cli
// builds itself: the application is that project's code, which the CLI cannot
// link into itself, so it cannot construct that project's App in process.
// Instead it shells out to the go tool in that project, the same way a
// developer would by hand, through an injectable CommandRunner so tests can
// assert the constructed command without starting a real process.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"text/tabwriter"

	"github.com/Elagoht/collage/internal/plugin"
)

// Version is the collage CLI's own version string, printed by "collage version".
// It identifies this tool, not any project it scaffolds or drives.
//
// Read from the build rather than written down, because a written-down version is
// one somebody has to remember to change and nobody does: this said 0.1.0 through
// four releases, so anyone who installed v0.4.1 was told they had the first one.
// "go install ...@v0.4.1" records that version in the binary, and this reports what
// is actually there.
var Version = buildVersion()

// buildVersion returns the module version this binary was built from: a tag for
// `go install ...@v1.2.3`, a pseudo-version for a build from a git checkout, and
// "devel" when the build carries no version at all (-buildvcs=false, or not in a
// repository) — the honest answer for a binary whose source may be anything.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return "devel"
	}
	return strings.TrimPrefix(info.Main.Version, "v")
}

// ErrNoCommand is returned when Run is called with no arguments at all.
var ErrNoCommand = errors.New("collage: no command given")

// ErrUnknownCommand is returned when the requested command name matches
// neither a built-in command nor a registered plugin command.
var ErrUnknownCommand = errors.New("collage: unknown command")

// ErrReservedCommandName is returned when a CLI.Commands entry's Name matches
// a built-in command ("new", "dev", "build", "export", "serve", "inspect", "version", or
// "help"). A plugin is not permitted to shadow a built-in command.
var ErrReservedCommandName = errors.New("collage: command name collides with a built-in command")

// ErrDuplicateCommand is returned when two entries in CLI.Commands share the
// same Name.
var ErrDuplicateCommand = errors.New("collage: duplicate command name")

// ErrEmptyCommandName is returned when a CLI.Commands entry has an empty Name.
var ErrEmptyCommandName = errors.New("collage: empty command name")

// CLI is the collage command-line tool. It dispatches a command by name,
// running either one of the built-ins (new, dev, build, version, help) or one
// of Commands, and returns an exit code rather than terminating the process.
//
// The zero value is usable: Stdout and Stderr default to os.Stdout and
// os.Stderr, and Runner defaults to a CommandRunner that starts a real
// process.
type CLI struct {
	// Stdout is where command output is written. A nil Stdout means
	// os.Stdout.
	Stdout io.Writer
	// Stderr is where usage text and error output is written. A nil Stderr
	// means os.Stderr.
	Stderr io.Writer
	// Stdin is where a command that asks a question reads the answer. A nil
	// Stdin means os.Stdin; only "build -i" reads it.
	Stdin io.Reader
	// Runner runs the external "go" commands the dev and build commands
	// construct. A nil Runner means a CommandRunner that starts a real
	// process; tests substitute a fake to assert the constructed command
	// without starting one.
	Runner CommandRunner
	// Commands lists the CLI subcommands a plugin contributed through
	// plugin.Host.RegisterCommand, in the order they should be listed and
	// dispatched. Run rejects the whole invocation — see
	// ErrReservedCommandName, ErrDuplicateCommand, and ErrEmptyCommandName —
	// if any entry is malformed, rather than silently dropping it.
	Commands []plugin.Command
}

// builtinDescription is one row of the built-in command table Run dispatches
// against and printUsage lists.
type builtinDescription struct {
	name  string
	short string
}

// builtins lists every command Run handles itself, in the order help and the
// usage text present them.
var builtins = []builtinDescription{
	{"new", "Scaffold a new collage project"},
	{"dev", "Run the current directory's project in development mode"},
	{"build", "Compile the current directory's project into the binary you deploy"},
	{"export", "Render the current directory's project to static files"},
	{"serve", "Serve a static export the way a static host would"},
	{"inspect", "Print what the current directory's project is made of, as JSON"},
	{"version", "Print the collage CLI version"},
	{"help", "Show help for a command, or list every command"},
}

// Run dispatches args[0] as a command name and runs it with the remaining
// arguments, returning the process exit code: 0 on success, 2 for a usage
// error (no command, an unknown command, or a malformed CLI.Commands), and 1
// for a command that parsed correctly but failed to do its work.
//
// Run never calls os.Exit; that is main's job, and main's only job.
func (c *CLI) Run(ctx context.Context, args []string) int {
	if err := c.validateCommands(); err != nil {
		fmt.Fprintf(c.stderr(), "collage: %v\n", err)
		return 2
	}

	if len(args) == 0 {
		fmt.Fprintln(c.stderr(), ErrNoCommand)
		fmt.Fprintln(c.stderr())
		c.printUsage(c.stderr())
		return 2
	}

	name, rest := args[0], args[1:]

	switch name {
	case "-h", "-help", "--help":
		// Asked for, so not a usage error: the usage on stdout and success.
		c.printUsage(c.stdout())
		return 0
	case "help":
		return c.runHelp(rest)
	case "version":
		fmt.Fprintf(c.stdout(), "collage version %s\n", Version)
		return 0
	case "new":
		return c.runNew(rest)
	case "dev":
		return c.runDev(ctx, rest)
	case "build":
		return c.runBuild(ctx, rest)
	case "export":
		return c.runExport(ctx, rest)
	case "serve":
		return c.runServe(ctx, rest)
	case "inspect":
		return c.runInspect(ctx, rest)
	}

	if cmd, ok := c.findPluginCommand(name); ok {
		if err := cmd.Run(ctx, rest); err != nil {
			fmt.Fprintf(c.stderr(), "collage: %s: %v\n", name, err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(c.stderr(), "%s: %q\n", ErrUnknownCommand, name)
	fmt.Fprintln(c.stderr())
	c.printUsage(c.stderr())
	return 2
}

// runHelp implements the "help" command: with no arguments it lists every
// command on Stdout and returns 0, the same as a successful command; with a
// command name it prints that command's own usage.
func (c *CLI) runHelp(args []string) int {
	if len(args) == 0 {
		c.printUsage(c.stdout())
		return 0
	}
	if err := c.printCommandHelp(c.stdout(), args[0]); err != nil {
		// The error already names the tool.
		fmt.Fprintln(c.stderr(), err)
		return 2
	}
	return 0
}

// validateCommands reports whether CLI.Commands is well-formed: every entry
// has a non-empty Name (ErrEmptyCommandName), no entry's Name collides with a
// built-in (ErrReservedCommandName), and no two entries share a Name
// (ErrDuplicateCommand). Run refuses to dispatch anything at all when this
// fails, the same way a misconfigured application refuses to start rather
// than serving with an ambiguous command table.
func (c *CLI) validateCommands() error {
	seen := make(map[string]bool, len(c.Commands))
	for _, cmd := range c.Commands {
		if cmd.Name == "" {
			return ErrEmptyCommandName
		}
		if isBuiltin(cmd.Name) {
			return fmt.Errorf("%w: %q", ErrReservedCommandName, cmd.Name)
		}
		if seen[cmd.Name] {
			return fmt.Errorf("%w: %q", ErrDuplicateCommand, cmd.Name)
		}
		seen[cmd.Name] = true
	}
	return nil
}

// isBuiltin reports whether name is one of the commands Run handles itself —
// those listed in builtins.
func isBuiltin(name string) bool {
	for _, b := range builtins {
		if b.name == name {
			return true
		}
	}
	return false
}

// findPluginCommand returns the CLI.Commands entry named name, and whether one
// was found.
func (c *CLI) findPluginCommand(name string) (plugin.Command, bool) {
	for _, cmd := range c.Commands {
		if cmd.Name == name {
			return cmd, true
		}
	}
	return plugin.Command{}, false
}

// printUsage writes the top-level usage text — what collage is, how to invoke
// it, and every available command, built-in and plugin-contributed alike — to
// w.
func (c *CLI) printUsage(w io.Writer) {
	fmt.Fprintln(w, "collage is the CLI for collage, a Go framework for server-side component-based rendering.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  collage <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, b := range builtins {
		fmt.Fprintf(tw, "  %s\t%s\n", b.name, b.short)
	}
	for _, cmd := range c.Commands {
		fmt.Fprintf(tw, "  %s\t%s\n", cmd.Name, cmd.Short)
	}
	tw.Flush()

	fmt.Fprintln(w)
	fmt.Fprintln(w, `Run "collage help <command>" for more information about a command.`)
}

// printCommandHelp writes name's own usage text to w, or returns
// ErrUnknownCommand if name is neither a built-in nor a registered plugin
// command.
func (c *CLI) printCommandHelp(w io.Writer, name string) error {
	switch name {
	case "new":
		fmt.Fprint(w, newUsage)
		return nil
	case "dev":
		fmt.Fprint(w, devUsage)
		return nil
	case "build":
		fmt.Fprint(w, buildUsage)
		return nil
	case "export":
		fmt.Fprint(w, exportUsage)
		return nil
	case "serve":
		fmt.Fprint(w, serveUsage)
		return nil
	case "inspect":
		fmt.Fprint(w, inspectUsage)
		return nil
	case "version":
		fmt.Fprintln(w, "Usage: collage version")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Prints the collage CLI's own version string.")
		return nil
	case "help":
		fmt.Fprintln(w, "Usage: collage help [command]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "With no command, lists every available command. With a command name, prints that command's own usage.")
		return nil
	}

	if cmd, ok := c.findPluginCommand(name); ok {
		fmt.Fprintf(w, "Usage: collage %s\n\n%s\n", cmd.Usage, cmd.Short)
		return nil
	}

	return fmt.Errorf("%w: %q", ErrUnknownCommand, name)
}

// stdout returns c.Stdout, or os.Stdout when it is nil.
func (c *CLI) stdout() io.Writer {
	if c.Stdout != nil {
		return c.Stdout
	}
	return os.Stdout
}

// stderr returns c.Stderr, or os.Stderr when it is nil.
func (c *CLI) stderr() io.Writer {
	if c.Stderr != nil {
		return c.Stderr
	}
	return os.Stderr
}

// runner returns c.Runner, or a CommandRunner that starts a real process when
// it is nil.
func (c *CLI) runner() CommandRunner {
	if c.Runner != nil {
		return c.Runner
	}
	return execRunner{}
}

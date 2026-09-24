package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrMissingProjectName is returned by the "new" command when it is called
// with no project name.
var ErrMissingProjectName = errors.New("collage: missing project name")

// ErrUnknownTemplate is returned by the "new" command for a -template it has no
// scaffold for.
var ErrUnknownTemplate = errors.New("collage: unknown template")

// ErrTargetNotEmpty is returned by the "new" command when its target
// directory already has contents and -force was not given.
var ErrTargetNotEmpty = errors.New("collage: target directory is not empty")

// newUsage is "collage help new"'s own usage text.
const newUsage = `Usage: collage new <name> [--template demo|minimal] [--dir path] [--module path] [--force]

Scaffolds a new, runnable collage project named <name>.

  demo      the default: a home page, a page of live demos (an API action, a
            form, a fragment with its own URL, a JSON document), a not-found
            page, their tests, a .env.example for "collage dev", a .gitignore
            and a README
  minimal   one layout around one page saying hello, and a stylesheet with a
            dark mode — the same main.go, with nothing to delete before you start

  --template name   the project to scaffold: demo or minimal (default: demo)
  --dir path        directory to scaffold into (default: ./<name>)
  --module path     the scaffolded go.mod's module path (default: <name>)
  --force           scaffold into a non-empty directory anyway

Flags take one dash or two: -template and --template are the same flag.
`

// newValueFlags names the "new" command's flags that consume a following
// argument, for splitPositional.
var newValueFlags = map[string]bool{"dir": true, "module": true, "template": true}

// runNew implements the "new" command.
func (c *CLI) runNew(args []string) int {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.SetOutput(c.stderr())
	fs.Usage = func() { fmt.Fprint(c.stderr(), newUsage) }
	dir := fs.String("dir", "", "directory to scaffold into")
	module := fs.String("module", "", "the scaffolded go.mod's module path")
	force := fs.Bool("force", false, "scaffold into a non-empty directory anyway")
	template := fs.String("template", variantDemo, "the project to scaffold: demo or minimal")

	// The stdlib flag package stops parsing at the first non-flag argument, so
	// a flag placed after <name> — which is exactly how newUsage documents
	// this command — would otherwise never be seen. positional and flagArgs
	// separate the project name from the flags surrounding it before Parse
	// ever runs, so their order relative to each other does not matter.
	positional, flagArgs := splitPositional(args, newValueFlags)

	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if len(positional) != 1 {
		fmt.Fprintln(c.stderr(), ErrMissingProjectName)
		fmt.Fprintln(c.stderr())
		fs.Usage()
		return 2
	}
	name := positional[0]
	variant := *template
	if variant != variantDemo && variant != variantMinimal {
		fmt.Fprintf(c.stderr(), "%v: %q; the templates are %q and %q\n", ErrUnknownTemplate, variant, variantDemo, variantMinimal)
		return 2
	}

	targetDir := *dir
	if targetDir == "" {
		targetDir = filepath.Join(".", name)
	}
	modulePath := *module
	if modulePath == "" {
		modulePath = name
	}

	if err := checkTarget(targetDir, *force); err != nil {
		fmt.Fprintf(c.stderr(), "collage: %v\n", err)
		return 1
	}
	if err := writeScaffold(targetDir, modulePath, name, variant); err != nil {
		fmt.Fprintf(c.stderr(), "collage: scaffold: %v\n", err)
		return 1
	}

	out := c.stdout()
	fmt.Fprintf(out, "Scaffolded %q in %s\n\n", name, targetDir)
	fmt.Fprintln(out, "Next steps:")
	fmt.Fprintf(out, "  cd %s\n", targetDir)
	fmt.Fprintln(out, "  go mod tidy")
	if variant == variantDemo {
		fmt.Fprintln(out, "  cp .env.example .env.development")
	}
	fmt.Fprintln(out, "  collage dev")
	return 0
}

// splitPositional splits args into its non-flag arguments and its flags (plus
// the values that follow a flag in valueFlags), preserving each group's
// relative order. A flag given as "-name=value" is recognised without
// consulting valueFlags at all, since it carries its own value; a flag given
// as "-name value" consumes the following argument only when valueFlags[name]
// is true, so a boolean flag's own value is never mistaken for one.
//
// This exists because the stdlib flag package parses only a leading run of
// flags and treats everything from the first non-flag argument on as
// positional — so "collage new demo -dir x", with the positional argument
// first, would otherwise leave -dir unparsed. Splitting first, then handing
// flagArgs alone to a FlagSet, makes the argument order flag.Parse expects
// irrelevant to the command's own users.
func splitPositional(args []string, valueFlags map[string]bool) (positional, flagArgs []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}

		flagArgs = append(flagArgs, arg)
		if strings.Contains(arg, "=") {
			continue
		}
		name := strings.TrimLeft(arg, "-")
		if valueFlags[name] && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return positional, flagArgs
}

// checkTarget refuses a non-empty dir unless force is set. A directory that
// does not exist yet, or exists and is empty, is always fine.
func checkTarget(dir string, force bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(entries) > 0 && !force {
		return fmt.Errorf("%w: %s", ErrTargetNotEmpty, dir)
	}
	return nil
}

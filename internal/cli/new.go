package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Elagoht/collage/internal/cli/plugincontract"
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
const newUsage = `Usage: collage new <name> [--template minimal|demo] [--with plugins] [--dir path] [--module path] [--force] [--ai]

Scaffolds a new, runnable collage project named <name>.

  minimal   the default: one layout around one page saying hello, and a
            stylesheet with a dark mode — nothing to delete before you start
  demo      the same main.go, with a home page, a page of live demos (an API
            action, a form, a fragment with its own URL, a JSON document), a
            not-found page, their tests, a .env.example for "collage dev", a
            .gitignore and a README

  --template name   the project to scaffold: minimal or demo (default: minimal)
  --with plugins    plugins to add once it is scaffolded, as "collage plugin add"
                    would: @bundles and plugins, separated by commas or given
                    in more than one --with; "collage help plugin" lists the
                    bundles
  --dir path        directory to scaffold into (default: ./<name>)
  --module path     the scaffolded go.mod's module path (default: <name>)
  --force           scaffold into a non-empty directory anyway
  --ai              write AGENTS.md, the rules for coding agents, and a
                    CLAUDE.md importing it; "collage help ai" says more

Flags take one dash or two: -template and --template are the same flag.
`

// newValueFlags names the "new" command's flags that consume a following
// argument, for splitPositional.
var newValueFlags = map[string]bool{"dir": true, "module": true, "template": true, "with": true}

// withFlag is --with: every value given, each split at its commas.
type withFlag []string

func (w *withFlag) String() string { return strings.Join(*w, ",") }

func (w *withFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*w = append(*w, p)
		}
	}
	return nil
}

// prepareWith readies a project new has just scaffolded for "plugin add":
// it requires the collage this CLI is, so a plugin built against an older
// one does not pull the project back to it. A variable so the tests can point
// the project at this repository instead.
var prepareWith = func(ctx context.Context, c *CLI, dir string) error {
	if _, errOut, err := c.goOutput(ctx, dir, "get", "--", "github.com/Elagoht/collage@"+collageQuery(Version)); err != nil {
		return goError(errOut, err)
	}
	return nil
}

// collageQuery is the version of collage to ask go get for, given this CLI's
// version: the release it is, or latest for a build that is not one — a
// checkout, a pseudo-version, or a tree with changes (+dirty) — which go get
// could not fetch.
func collageQuery(version string) string {
	v := "v" + version
	if version == "devel" || strings.Contains(version, "+") || plugincontract.IsPseudoVersion(v) {
		return "latest"
	}
	return v
}

// runNew implements the "new" command.
func (c *CLI) runNew(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.SetOutput(c.stderr())
	fs.Usage = func() { fmt.Fprint(c.stderr(), newUsage) }
	dir := fs.String("dir", "", "directory to scaffold into")
	module := fs.String("module", "", "the scaffolded go.mod's module path")
	force := fs.Bool("force", false, "scaffold into a non-empty directory anyway")
	ai := fs.Bool("ai", false, "write AGENTS.md and CLAUDE.md for coding agents")
	template := fs.String("template", variantMinimal, "the project to scaffold: minimal or demo")
	var with withFlag
	fs.Var(&with, "with", "plugins to add: @bundles and plugins, comma-separated")

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
		fmt.Fprintf(c.stderr(), "%v: %q; the templates are %q and %q\n", ErrUnknownTemplate, variant, variantMinimal, variantDemo)
		return 2
	}

	withGiven := false
	fs.Visit(func(f *flag.Flag) { withGiven = withGiven || f.Name == "with" })
	if withGiven && len(with) == 0 {
		fmt.Fprintln(c.stderr(), "collage: new: --with: want a plugin or a @bundle")
		return 2
	}
	var refs []namedRef
	if len(with) > 0 {
		var err error
		if refs, err = expandRefs(with); err != nil {
			fmt.Fprintf(c.stderr(), "collage: new: --with: %v\n", err)
			return 2
		}
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
	overwritten, err := existingScaffoldFiles(targetDir, variant)
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: scaffold: %v\n", err)
		return 1
	}
	if err := writeScaffold(targetDir, modulePath, name, variant); err != nil {
		fmt.Fprintf(c.stderr(), "collage: scaffold: %v\n", err)
		return 1
	}

	out := c.stdout()
	fmt.Fprintf(out, "Scaffolded %q in %s\n", name, targetDir)
	if len(overwritten) > 0 {
		fmt.Fprintf(out, "Overwrote what was there: %s\n", strings.Join(overwritten, ", "))
	}
	if *ai {
		done, err := writeAIFiles(targetDir, Version)
		if err != nil {
			fmt.Fprintf(c.stderr(), "collage: new: --ai: %v\n", err)
			return 1
		}
		c.printAIWritten(done, targetDir)
	}
	fmt.Fprintln(out)
	code, tidied := 0, false
	if len(refs) > 0 {
		if err := prepareWith(ctx, c, targetDir); err != nil {
			fmt.Fprintf(c.stderr(), "collage: new: --with: %v\n", err)
			code = 1
		} else {
			code, tidied = c.addPlugins(ctx, targetDir, refs)
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintln(out, "Next steps:")
	if !sameDir(targetDir, ".") {
		fmt.Fprintf(out, "  cd %s\n", targetDir)
	}
	if !tidied {
		fmt.Fprintln(out, "  go mod tidy")
	}
	if variant == variantDemo {
		fmt.Fprintln(out, "  cp .env.example .env.development")
	}
	fmt.Fprintln(out, "  collage dev")
	return code
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

// sameDir reports whether a and b name the same directory.
func sameDir(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
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

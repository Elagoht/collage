package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Elagoht/collage/internal/cli/plugincontract"
)

// pluginUsage is "collage help plugin"'s own usage text.
const pluginUsage = `Usage:
  collage plugin add <owner/name | module path>[@version] [-dir dir]
  collage plugin del <owner/name | module path> [-dir dir]
  collage plugin check [-release] [-run] [dir]

add fetches a plugin with go get, checks that it is one, and adds it to
plugins.go and an empty section for it to plugins-config.json. owner/name means
github.com/<owner>/collage-<name>; a full module path is used as written.

del removes the plugin from plugins.go and plugins-config.json, printing a
section that held settings, and runs go mod tidy.

check checks a plugin repository against the contract add enforces; with
-release it also wants a release tag at HEAD, and with -run it builds the
plugin into a throwaway application.

  -dir   the directory of the project's package main (default ".")

The contract: ` + plugincontract.AuthorsURL + `
`

// pluginValueFlags names the plugin subcommands' flags that consume a
// following argument, for splitPositional.
var pluginValueFlags = map[string]bool{"dir": true}

// addedPlugin is one plugin "plugin add" wired into the project.
type addedPlugin struct {
	Name    string // the manifest's name, owner/name
	Version string // the module version go get chose
	Call    string // the entry written into plugins.go
	Setup   string // the manifest's setup note, or ""
	// ConfigAdded is whether plugins-config.json gained a section for it;
	// false when one was already there and was kept.
	ConfigAdded bool
}

// runPlugin implements the "plugin" command: it dispatches its subcommand.
func (c *CLI) runPlugin(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(c.stderr(), "collage: plugin: want a subcommand: add")
		fmt.Fprintln(c.stderr())
		fmt.Fprint(c.stderr(), pluginUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		fmt.Fprint(c.stdout(), pluginUsage)
		return 0
	case "add":
		return c.runPluginAdd(ctx, args[1:])
	}
	fmt.Fprintf(c.stderr(), "collage: plugin: unknown subcommand %q\n\n", args[0])
	fmt.Fprint(c.stderr(), pluginUsage)
	return 2
}

// runPluginAdd implements "plugin add": every file it touches is put back as
// it was when a step fails.
func (c *CLI) runPluginAdd(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("plugin add", flag.ContinueOnError)
	flags.SetOutput(c.stderr())
	flags.Usage = func() { fmt.Fprint(c.stderr(), pluginUsage) }
	dir := flags.String("dir", ".", "the directory of the project's package main")

	positional, flagArgs := splitPositional(args, pluginValueFlags)
	if err := flags.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	positional = append(positional, flags.Args()...)
	if len(positional) != 1 {
		fmt.Fprintln(c.stderr(), "collage: plugin add: want one plugin: owner/name, or a module path")
		fmt.Fprintln(c.stderr())
		flags.Usage()
		return 2
	}
	ref, err := plugincontract.ParseRef(positional[0])
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: plugin add: %v\n", err)
		return 2
	}

	fail := func(err error) int {
		fmt.Fprintf(c.stderr(), "collage: plugin add: %v\n", err)
		return 1
	}
	if err := checkPackageMain(*dir); err != nil {
		return fail(err)
	}
	if _, err := os.Stat(filepath.Join(*dir, "go.mod")); err != nil {
		return fail(fmt.Errorf("no go.mod in %s", *dir))
	}
	snap, err := snapshot(
		filepath.Join(*dir, "go.mod"),
		filepath.Join(*dir, "go.sum"),
		filepath.Join(*dir, "plugins.go"),
		filepath.Join(*dir, "plugins-config.json"),
	)
	if err != nil {
		return fail(err)
	}

	added, err := c.pluginAdd(ctx, *dir, ref, map[string]bool{})
	if err == nil {
		err = c.goTidy(ctx, *dir)
	}
	if err != nil {
		if rerr := snap.restore(); rerr != nil {
			err = errors.Join(err, fmt.Errorf("putting the files back: %w", rerr))
		}
		return fail(err)
	}

	c.printAdded(added)
	if !callsPlugins(*dir) {
		fmt.Fprintln(c.stdout())
		fmt.Fprintln(c.stdout(), "plugins.go is new: give it to collage.New once —")
		fmt.Fprintln(c.stdout(), "\tPlugins: plugins(),")
	}
	return 0
}

// pluginAdd fetches ref into the project at dir, checks it against the
// contract, and adds it to plugins.go and plugins-config.json. It changes
// files; the caller snapshots them first and restores them on an error.
// visiting holds the plugins being added further up a chain of requires.
func (c *CLI) pluginAdd(ctx context.Context, dir string, ref plugincontract.Ref, visiting map[string]bool) ([]addedPlugin, error) {
	version := ref.Version
	if version == "" {
		version = "latest"
	}
	if _, errOut, err := c.goOutput(ctx, dir, "get", "--", ref.Path+"@"+version); err != nil {
		return nil, goError(errOut, err)
	}
	info, err := c.listModule(ctx, dir, ref.Path)
	if err != nil {
		return nil, err
	}
	label := ref.Short
	if label == "" {
		label = ref.Path
	}
	p, err := plugincontract.Check(info.Dir, ref.Short)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	if plugincontract.IsPseudoVersion(info.Version) {
		return nil, fmt.Errorf("%s %s is not a release; tag one (git tag v0.1.0) and push it", info.Path, info.Version)
	}

	pluginsPath := filepath.Join(dir, "plugins.go")
	src, err := readIfExists(pluginsPath)
	if err != nil {
		return nil, err
	}
	f, err := parsePluginsFile(src)
	if err != nil {
		return nil, err
	}
	var orders []int
	for _, e := range f.entries() {
		orders = append(orders, c.pluginOrder(ctx, dir, e.Import))
	}
	ident := f.insert(info.Path, p.Constructor.Package, p.Constructor.Call, insertIndex(orders, p.Manifest.Order))
	out, err := f.format()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(pluginsPath, out, 0o644); err != nil {
		return nil, err
	}

	configPath := filepath.Join(dir, "plugins-config.json")
	config, err := readIfExists(configPath)
	if err != nil {
		return nil, err
	}
	config, configAdded, err := addConfigSection(config, p.Manifest.Name)
	if err != nil {
		return nil, err
	}
	if configAdded {
		if err := os.WriteFile(configPath, config, 0o644); err != nil {
			return nil, err
		}
	}

	return []addedPlugin{{
		Name:        p.Manifest.Name,
		Version:     info.Version,
		Call:        p.Constructor.Call(ident),
		Setup:       p.Manifest.Setup,
		ConfigAdded: configAdded,
	}}, nil
}

// pluginOrder is the manifest order of the module importPath in the project
// at dir; 0 when the module or its manifest cannot be read.
func (c *CLI) pluginOrder(ctx context.Context, dir, importPath string) int {
	info, err := c.listModule(ctx, dir, importPath)
	if err != nil {
		return 0
	}
	m, err := plugincontract.ReadManifest(info.Dir)
	if err != nil {
		return 0
	}
	return m.Order
}

// goTidy runs "go mod tidy" in dir.
func (c *CLI) goTidy(ctx context.Context, dir string) error {
	if _, errOut, err := c.goOutput(ctx, dir, "mod", "tidy"); err != nil {
		return goError(errOut, err)
	}
	return nil
}

// goError is the go tool's own message for a failed run, or the run's error
// when it printed nothing.
func goError(errOut []byte, err error) error {
	if msg := bytes.TrimSpace(errOut); len(msg) > 0 {
		return errors.New(string(msg))
	}
	return err
}

// printAdded writes what add did, a paragraph per plugin.
func (c *CLI) printAdded(added []addedPlugin) {
	w := c.stdout()
	for i, a := range added {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "added %s %s\n", a.Name, a.Version)
		config := strconv.Quote(a.Name) + ": {}"
		if !a.ConfigAdded {
			config = strconv.Quote(a.Name) + " kept as it was"
		}
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "  plugins.go\t%s\n", a.Call)
		fmt.Fprintf(tw, "  plugins-config.json\t%s\n", config)
		tw.Flush()
		if a.Setup != "" {
			fmt.Fprintln(w)
			fmt.Fprintln(w, strings.TrimSpace(a.Setup))
		}
	}
}

// readIfExists is the file's content, or nil when there is no such file.
func readIfExists(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// goSources are the non-test Go files directly in dir.
func goSources(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.Type().IsRegular() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out, nil
}

// checkPackageMain refuses a dir with no non-test Go file declaring package
// main: plugins.go belongs next to the program's main.
func checkPackageMain(dir string) error {
	files, err := goSources(dir)
	if err != nil {
		return err
	}
	for _, path := range files {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly)
		if err == nil && f.Name.Name == "main" {
			return nil
		}
	}
	return fmt.Errorf("%s holds no package main; pass the project's directory with -dir", dir)
}

// callsPlugins reports whether a non-test Go file in dir other than plugins.go
// calls plugins(): whether the list plugins.go returns reaches collage.New.
func callsPlugins(dir string) bool {
	files, err := goSources(dir)
	if err != nil {
		return false
	}
	for _, path := range files {
		if filepath.Base(path) == "plugins.go" {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			continue
		}
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && len(call.Args) == 0 {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "plugins" {
					found = true
				}
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

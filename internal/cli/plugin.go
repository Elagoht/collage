package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
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
	// Already is whether plugins.go had it before: only its version moved.
	Already bool
	// Moved is whether, for a plugin already there, go.mod's requirement
	// of it changed.
	Moved bool
}

// runPlugin implements the "plugin" command: it dispatches its subcommand.
func (c *CLI) runPlugin(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(c.stderr(), "collage: plugin: want a subcommand: add, del")
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
	case "del":
		return c.runPluginDel(ctx, args[1:])
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

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	guard, commit := c.restoreUnlessCommitted("plugin add", snap)
	defer guard()

	added, err := c.pluginAdd(ctx, *dir, ref, map[string]bool{})
	if err == nil {
		err = c.goTidy(ctx, *dir)
	}
	if err != nil {
		return fail(err)
	}
	commit()

	c.printAdded(added)
	if !callsPlugins(*dir) {
		fmt.Fprintln(c.stdout())
		fmt.Fprintln(c.stdout(), "plugins.go is new: give it to collage.New once —")
		fmt.Fprintln(c.stdout(), "\tPlugins: plugins(),")
	}
	return 0
}

// restoreUnlessCommitted returns a guard to defer and a commit to call once
// the command has succeeded. Unless it was, the guard puts the snapshot back;
// Ctrl-C stops the go child rather than this process, so that happens too,
// and a panic in a step puts the files back, then goes on.
func (c *CLI) restoreUnlessCommitted(cmd string, snap fileSnapshot) (guard, commit func()) {
	done := false
	guard = func() {
		if done {
			return
		}
		r := recover()
		if rerr := snap.restore(); rerr != nil {
			fmt.Fprintf(c.stderr(), "collage: %s: putting the files back: %v\n", cmd, rerr)
		}
		if r != nil {
			panic(r)
		}
	}
	return guard, func() { done = true }
}

// pluginAdd fetches ref into the project at dir, checks it against the
// contract, adds the plugins it requires, then adds it to plugins.go and
// plugins-config.json; a plugin already in plugins.go keeps its entry, and
// only its version moves. It changes files; the caller snapshots them first
// and restores them on an error. visiting holds the manifest names of the
// plugins being added further up a chain of requires.
func (c *CLI) pluginAdd(ctx context.Context, dir string, ref plugincontract.Ref, visiting map[string]bool) ([]addedPlugin, error) {
	gomod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return nil, err
	}
	path, err := c.goGet(ctx, dir, ref)
	if err != nil {
		return nil, err
	}
	info, err := c.listModule(ctx, dir, path)
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

	name := p.Manifest.Name
	visiting[name] = true
	defer delete(visiting, name)
	var added []addedPlugin
	for _, req := range p.Manifest.Requires {
		present, err := c.attributable(ctx, dir, req)
		if err != nil {
			return nil, err
		}
		if present {
			continue
		}
		if visiting[req] {
			return nil, &cycleError{chain: []string{name, req}, closing: req}
		}
		more, err := c.pluginAdd(ctx, dir, plugincontract.Ref{Path: plugincontract.ConventionalPath(req), Short: req}, visiting)
		var cycle *cycleError
		if errors.As(err, &cycle) && !cycle.closed() {
			cycle.chain = append([]string{name}, cycle.chain...)
		}
		if err != nil {
			return nil, err
		}
		added = append(added, more...)
	}
	if len(added) > 0 {
		// A required plugin's own requirements may have moved this one up.
		final, err := c.listModule(ctx, dir, info.Path)
		if err != nil {
			return nil, err
		}
		if final.Version != info.Version {
			if p, err = plugincontract.Check(final.Dir, ref.Short); err != nil {
				return nil, fmt.Errorf("%s: %w", label, err)
			}
			if plugincontract.IsPseudoVersion(final.Version) {
				return nil, fmt.Errorf("%s %s is not a release; tag one (git tag v0.1.0) and push it", final.Path, final.Version)
			}
		}
		info = final
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
	if f.has(info.Path) {
		a := addedPlugin{Name: name, Version: info.Version, Already: true}
		a.Moved = requiredVersion(gomod, info.Path) != info.Version
		return append(added, a), nil
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
	config, configAdded, err := addConfigSection(config, name)
	if err != nil {
		return nil, err
	}
	if configAdded {
		if err := os.WriteFile(configPath, config, 0o644); err != nil {
			return nil, err
		}
	}

	return append(added, addedPlugin{
		Name:        name,
		Version:     info.Version,
		Call:        p.Constructor.Call(ident),
		Setup:       p.Manifest.Setup,
		ConfigAdded: configAdded,
	}), nil
}

// declaredPath is the go tool's complaint that a module's go.mod names it
// differently from the path it was fetched by.
var declaredPath = regexp.MustCompile(`module declares its path as: (\S+)`)

// goGet runs go get for ref and returns the module path it is known by. A
// module fetched by a path differing only in case from the one its go.mod
// declares (owner/name is lowercased; a GitHub owner may not be) is fetched
// again by the declared path, once.
func (c *CLI) goGet(ctx context.Context, dir string, ref plugincontract.Ref) (string, error) {
	// upgrade is latest for a module go.mod lacks, and never moves one it
	// has down: from a prerelease, say, to an older release.
	version := ref.Version
	if version == "" {
		version = "upgrade"
	}
	_, errOut, err := c.goOutput(ctx, dir, "get", "--", ref.Path+"@"+version)
	if err == nil {
		return ref.Path, nil
	}
	m := declaredPath.FindSubmatch(errOut)
	if m == nil || !strings.EqualFold(string(m[1]), ref.Path) {
		return "", goError(errOut, err)
	}
	declared := string(m[1])
	if _, errOut2, err2 := c.goOutput(ctx, dir, "get", "--", declared+"@"+version); err2 != nil {
		return "", fmt.Errorf("%s declares its module path as %s, and fetching that failed: %w", ref.Path, declared, goError(errOut2, err2))
	}
	return declared, nil
}

// attributable reports whether plugins.go already has an entry for the plugin
// named name: one importing its conventional path, in any case, or one whose
// module's manifest gives that name.
func (c *CLI) attributable(ctx context.Context, dir, name string) (bool, error) {
	src, err := readIfExists(filepath.Join(dir, "plugins.go"))
	if err != nil || src == nil {
		return false, err
	}
	f, err := parsePluginsFile(src)
	if err != nil {
		return false, err
	}
	conventional := plugincontract.ConventionalPath(name)
	entries := f.entries()
	for _, e := range entries {
		if strings.EqualFold(e.Import, conventional) {
			return true, nil
		}
	}
	for _, e := range entries {
		info, err := c.listModule(ctx, dir, e.Import)
		if err != nil {
			continue
		}
		if m, err := plugincontract.ReadManifest(info.Dir); err == nil && m.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// cycleError is a chain of requires that comes back to a plugin on it. chain
// grows at the front as the error goes up the chain, until it starts with
// closing.
type cycleError struct {
	chain   []string
	closing string
}

func (e *cycleError) closed() bool { return len(e.chain) >= 2 && e.chain[0] == e.closing }

func (e *cycleError) Error() string { return "cycle: " + strings.Join(e.chain, " requires ") }

// requiredVersion is the version go.mod's text requires of module path, or "".
func requiredVersion(gomod []byte, path string) string {
	inBlock := false
	for _, line := range strings.Split(string(gomod), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		switch {
		case len(fields) == 0:
			continue
		case inBlock && fields[0] == ")":
			inBlock = false
			continue
		case !inBlock && fields[0] == "require":
			if len(fields) == 2 && fields[1] == "(" {
				inBlock = true
				continue
			}
			fields = fields[1:]
		case !inBlock:
			continue
		}
		if len(fields) == 2 && strings.EqualFold(strings.Trim(fields[0], `"`), path) {
			return fields[1]
		}
	}
	return ""
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
		if a.Already {
			if a.Moved {
				fmt.Fprintf(w, "%s is already added; now at %s\n", a.Name, a.Version)
			} else {
				fmt.Fprintf(w, "%s is already added\n", a.Name)
			}
			continue
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

// goSources are the non-test Go files directly in dir that go build compiles.
func goSources(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			continue
		}
		// MatchFile applies the file-name suffix rules and the //go:build
		// lines as go build does: a file it would not compile is not looked at.
		if ok, err := build.Default.MatchFile(dir, name); err != nil || !ok {
			continue
		}
		out = append(out, filepath.Join(dir, name))
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

// delEntry is one plugin in plugins.go, as far as it could be identified.
type delEntry struct {
	Import   string
	Name     string // the manifest's name, or ""
	Requires []string
}

// label is how the entry is named to the user.
func (e delEntry) label() string {
	if e.Name != "" {
		return e.Name
	}
	return e.Import
}

// runPluginDel implements "plugin del": every file it touches is put back as
// it was when a step fails.
func (c *CLI) runPluginDel(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("plugin del", flag.ContinueOnError)
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
		fmt.Fprintln(c.stderr(), "collage: plugin del: want one plugin: owner/name, or a module path")
		fmt.Fprintln(c.stderr())
		flags.Usage()
		return 2
	}
	ref, err := plugincontract.ParseRef(positional[0])
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: plugin del: %v\n", err)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(c.stderr(), "collage: plugin del: %v\n", err)
		return 1
	}
	if ref.Version != "" {
		return fail(fmt.Errorf("%s: del takes no version", positional[0]))
	}
	if err := checkPackageMain(*dir); err != nil {
		return fail(err)
	}
	pluginsPath := filepath.Join(*dir, "plugins.go")
	src, err := readIfExists(pluginsPath)
	if err != nil {
		return fail(err)
	}
	if src == nil {
		return fail(fmt.Errorf("no plugins.go in %s; nothing to remove", *dir))
	}
	snap, err := snapshot(
		filepath.Join(*dir, "go.mod"),
		filepath.Join(*dir, "go.sum"),
		pluginsPath,
		filepath.Join(*dir, "plugins-config.json"),
	)
	if err != nil {
		return fail(err)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	guard, commit := c.restoreUnlessCommitted("plugin del", snap)
	defer guard()

	removed, settings, err := c.pluginDel(ctx, *dir, ref, src)
	if err == nil {
		err = c.goTidy(ctx, *dir)
	}
	if err != nil {
		return fail(err)
	}
	commit()

	fmt.Fprintf(c.stdout(), "removed %s\n", removed)
	if settings != "" {
		fmt.Fprintln(c.stdout(), "its settings were:")
		fmt.Fprintln(c.stdout(), settings)
	}
	return 0
}

// pluginDel removes the plugin ref names from plugins.go (src is its content)
// and its section from plugins-config.json. It returns the plugin's name and,
// when its section held settings, the section's raw value. It changes files;
// the caller snapshots them first and restores them on an error.
func (c *CLI) pluginDel(ctx context.Context, dir string, ref plugincontract.Ref, src []byte) (string, string, error) {
	f, err := parsePluginsFile(src)
	if err != nil {
		return "", "", err
	}
	var entries []delEntry
	for _, e := range f.entries() {
		d := delEntry{Import: e.Import}
		if info, err := c.listModule(ctx, dir, e.Import); err == nil {
			if m, err := plugincontract.ReadManifest(info.Dir); err == nil {
				d.Name, d.Requires = m.Name, m.Requires
			}
		}
		entries = append(entries, d)
	}
	target := -1
	for i, e := range entries {
		if ref.Short == "" && strings.EqualFold(e.Import, ref.Path) ||
			ref.Short != "" && (e.Name == ref.Short || strings.EqualFold(e.Import, ref.Path)) {
			target = i
			break
		}
	}
	if target < 0 {
		var have []string
		for _, e := range entries {
			have = append(have, e.label())
		}
		shown := "nothing"
		if len(have) > 0 {
			shown = strings.Join(have, ", ")
		}
		return "", "", fmt.Errorf("%s is not in plugins.go; it has: %s", firstNonEmpty(ref.Short, ref.Path), shown)
	}
	t := entries[target]
	name := t.Name
	if name == "" {
		name = ref.Short // the section's key, when the manifest cannot say
	}
	for i, e := range entries {
		if i == target || name == "" {
			continue
		}
		for _, r := range e.Requires {
			if r == name {
				return "", "", fmt.Errorf("%s requires %s; remove it first", e.label(), t.label())
			}
		}
	}

	if !f.remove(t.Import) {
		return "", "", fmt.Errorf("plugins.go: could not remove %s", t.Import)
	}
	out, err := f.format()
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "plugins.go"), out, 0o644); err != nil {
		return "", "", err
	}

	settings := ""
	if name != "" {
		configPath := filepath.Join(dir, "plugins-config.json")
		config, err := readIfExists(configPath)
		if err != nil {
			return "", "", err
		}
		if config != nil {
			config, removed, err := removeConfigSection(config, name)
			if err != nil {
				return "", "", err
			}
			if removed != nil {
				if err := os.WriteFile(configPath, config, 0o644); err != nil {
					return "", "", err
				}
				var compact bytes.Buffer
				if json.Compact(&compact, removed) != nil || compact.String() != "{}" {
					settings = string(removed)
				}
			}
		}
	}
	return t.label(), settings, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

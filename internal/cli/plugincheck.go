package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/Elagoht/collage/internal/ascii"
	"github.com/Elagoht/collage/internal/cli/plugincontract"
)

// runPluginCheck implements "plugin check".
func (c *CLI) runPluginCheck(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("plugin check", flag.ContinueOnError)
	flags.SetOutput(c.stderr())
	flags.Usage = func() { fmt.Fprint(c.stderr(), pluginUsage) }
	release := flags.Bool("release", false, "also want a release tag at HEAD")
	run := flags.Bool("run", false, "also build the plugin into a throwaway application and start it")

	positional, flagArgs := splitPositional(args, map[string]bool{})
	if err := flags.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	positional = append(positional, flags.Args()...)
	if len(positional) > 1 {
		fmt.Fprintln(c.stderr(), "collage: plugin check: want at most one directory, the plugin's")
		fmt.Fprintln(c.stderr())
		flags.Usage()
		return 2
	}
	dir := "."
	if len(positional) == 1 {
		dir = positional[0]
	}
	// Ctrl-C stops the go child, and the throwaway directory is still removed.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := c.pluginCheck(ctx, dir, *release, *run); err != nil {
		fmt.Fprintf(c.stderr(), "collage: plugin check: %v\n", err)
		return 1
	}
	return 0
}

// pluginCheck checks the plugin repository at dir against the contract add
// enforces; with release, that HEAD carries a release tag; with run, that the
// plugin builds into an application that starts.
func (c *CLI) pluginCheck(ctx context.Context, dir string, release, run bool) error {
	p, err := plugincontract.Check(dir, "")
	if err != nil {
		return err
	}
	name := p.Manifest.Name
	fmt.Fprintf(c.stdout(), "%s: meets the contract\n", name)
	if release {
		tag, err := c.releaseTag(ctx, dir)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.stdout(), "%s: %s at HEAD is a release\n", name, tag)
	}
	if run {
		return c.runPluginProgram(ctx, dir, p)
	}
	return nil
}

// releaseTag is the highest v* tag at dir's HEAD, which must be a release
// rather than a pseudo-version.
func (c *CLI) releaseTag(ctx context.Context, dir string) (string, error) {
	var out, errOut bytes.Buffer
	if err := c.runner().Run(ctx, "", nil, &out, &errOut, "git", "-C", dir, "tag", "--points-at", "HEAD"); err != nil {
		return "", fmt.Errorf("git tag: %w", goError(errOut.Bytes(), err))
	}
	best := ""
	for _, t := range strings.Fields(out.String()) {
		if !semverTag.MatchString(t) {
			continue
		}
		if best == "" || compareSemver(t, best) > 0 {
			best = t
		}
	}
	if best == "" {
		return "", errors.New("no release tag at HEAD; add rule 5 with git tag vX.Y.Z")
	}
	if plugincontract.IsPseudoVersion(best) {
		return "", fmt.Errorf("%s at HEAD is a pseudo-version, not a release; tag one with git tag vX.Y.Z", best)
	}
	return best, nil
}

// semverTag is a tag the go tool takes as a version: vMAJOR.MINOR.PATCH, a
// prerelease and build metadata optional.
var semverTag = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// compareSemver orders two tags semverTag matches, as semantic versioning
// does; build metadata is ignored.
func compareSemver(a, b string) int {
	ma, mb := semverTag.FindStringSubmatch(a), semverTag.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		if d := compareNumeric(ma[i], mb[i]); d != 0 {
			return d
		}
	}
	pa, pb := strings.TrimPrefix(ma[4], "-"), strings.TrimPrefix(mb[4], "-")
	switch {
	case pa == pb:
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	}
	ia, ib := strings.Split(pa, "."), strings.Split(pb, ".")
	for i := 0; i < len(ia) && i < len(ib); i++ {
		_, errA := strconv.ParseUint(ia[i], 10, 64)
		_, errB := strconv.ParseUint(ib[i], 10, 64)
		var d int
		switch {
		case errA == nil && errB == nil:
			d = compareNumeric(ia[i], ib[i])
		case errA == nil:
			d = -1
		case errB == nil:
			d = 1
		default:
			d = strings.Compare(ia[i], ib[i])
		}
		if d != 0 {
			return d
		}
	}
	return len(ia) - len(ib)
}

// compareNumeric orders two decimal strings without leading zeros.
func compareNumeric(a, b string) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

// runPluginProgram writes a program that builds an application around the
// plugin at dir, and the plugins it requires, into a directory inside dir,
// and runs it there with go run; the directory is removed afterwards.
func (c *CLI) runPluginProgram(ctx context.Context, dir string, p plugincontract.Plugin) error {
	name := p.Manifest.Name
	modulePath, err := readModulePath(dir)
	if err != nil {
		return err
	}
	var reqs []requiredPlugin
	if len(p.Manifest.Requires) > 0 {
		mods, err := c.listAllModules(ctx, dir)
		if err != nil {
			return err
		}
		for _, r := range p.Manifest.Requires {
			found := false
			for _, m := range mods {
				if !ascii.EqualFold(m.Path, plugincontract.ConventionalPath(r)) {
					continue
				}
				if m.Dir == "" {
					// go list -m does not download; go mod download says where it put it.
					if m, err = c.downloadModule(ctx, dir, m); err != nil {
						return fmt.Errorf("%s requires %s: %w", name, r, err)
					}
				}
				con, err := plugincontract.ReadConstructor(m.Dir)
				if err != nil {
					return fmt.Errorf("%s requires %s: %w", name, r, err)
				}
				reqs = append(reqs, requiredPlugin{Path: m.Path, Constructor: con})
				found = true
				break
			}
			if !found {
				fmt.Fprintf(c.stdout(), "%s: %s is not among its requirements; checked without it\n", name, r)
			}
		}
	}

	tmp, err := os.MkdirTemp(dir, "collage-plugin-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	src := checkProgram(name, modulePath, p.Constructor, reqs)
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte(src), 0o644); err != nil {
		return err
	}
	var errOut bytes.Buffer
	if err := c.runner().Run(ctx, dir, nil, c.stdout(), &errOut, "go", append(append([]string{"run"}, modFlag(dir)...), "./"+filepath.Base(tmp))...); err != nil {
		return goError(errOut.Bytes(), err)
	}
	return nil
}

// requiredPlugin is a plugin the checked one requires, as found among its
// module's requirements.
type requiredPlugin struct {
	Path        string
	Constructor plugincontract.Constructor
}

// checkProgram is the source of the program -run builds: it constructs the
// plugin as add would, checks its Name against the manifest, and starts an
// application with it and the plugins it requires before it.
func checkProgram(name, modulePath string, con plugincontract.Constructor, reqs []requiredPlugin) string {
	var imports, list strings.Builder
	for i, r := range reqs {
		ident := "req" + strconv.Itoa(i)
		fmt.Fprintf(&imports, "\t%s %s\n", ident, strconv.Quote(r.Path))
		list.WriteString(r.Constructor.Call(ident) + ", ")
	}
	q := strconv.Quote(name)
	return `// Code generated by collage plugin check; it is removed once it has run.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
	plugin ` + strconv.Quote(modulePath) + `
` + imports.String() + `)

func main() {
	p := ` + con.Call("plugin") + `
	if p.Name() != ` + q + ` {
		fmt.Fprintf(os.Stderr, "Name() is %q, collage.json says %q\n", p.Name(), ` + q + `)
		os.Exit(1)
	}
	// No templates of its own: an empty directory, so only the plugin's
	// own templates and pages are there.
	templates := fstest.MapFS{"templates": &fstest.MapFile{Mode: fs.ModeDir | 0o755}}
	app, err := collage.New(&collage.Config{
		Template:     collage.TemplateConfig{FS: templates, Root: "templates"},
		Plugins:      []collage.Plugin{` + list.String() + `p},
		PluginConfig: map[string]json.RawMessage{` + q + `: json.RawMessage("{}")},
	})
	if err == nil {
		err = app.Start()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "shutdown:", err)
		os.Exit(1)
	}
	fmt.Println(` + strconv.Quote(name+": builds and starts") + `)
}
`
}

// readModulePath is the module path dir's go.mod declares.
func readModulePath(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("no go.mod in %s", dir)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`), nil
		}
	}
	return "", fmt.Errorf("%s: go.mod has no module line", dir)
}

// downloadModule is "go mod download -json path@version" in dir.
func (c *CLI) downloadModule(ctx context.Context, dir string, m moduleInfo) (moduleInfo, error) {
	out, errOut, err := c.goOutput(ctx, dir, "mod", "download", "-json", "--", m.Path+"@"+m.Version)
	if err != nil {
		return moduleInfo{}, goError(errOut, err)
	}
	var got moduleInfo
	if err := json.Unmarshal(out, &got); err != nil {
		return moduleInfo{}, err
	}
	if got.Dir == "" {
		return moduleInfo{}, fmt.Errorf("go mod download gave no directory for %s@%s", m.Path, m.Version)
	}
	return got, nil
}

// modFlag is the go flag -run's go commands take in dir: -mod=readonly when the
// plugin vendors its dependencies. The go tool cannot list "all" from a vendor
// directory, and a required plugin the plugin's own code does not import is
// not vendored, so the module cache is used instead, without changing go.mod.
func modFlag(dir string) []string {
	if _, err := os.Stat(filepath.Join(dir, "vendor", "modules.txt")); err == nil {
		return []string{"-mod=readonly"}
	}
	return nil
}

// listAllModules is "go list -m -json all" in dir: the module and every
// module it requires.
func (c *CLI) listAllModules(ctx context.Context, dir string) ([]moduleInfo, error) {
	out, errOut, err := c.goOutput(ctx, dir, append(append([]string{"list"}, modFlag(dir)...), "-m", "-json", "all")...)
	if err != nil {
		return nil, goError(errOut, err)
	}
	var mods []moduleInfo
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var m moduleInfo
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			return mods, nil
		}
		if err != nil {
			return nil, err
		}
		mods = append(mods, m)
	}
}

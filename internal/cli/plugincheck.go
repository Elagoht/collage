package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/Elagoht/collage/internal/cli/plugincontract"
)

// runPluginCheck implements "plugin check".
func (c *CLI) runPluginCheck(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("plugin check", flag.ContinueOnError)
	flags.SetOutput(c.stderr())
	flags.Usage = func() { fmt.Fprint(c.stderr(), pluginUsage()) }
	release := flags.Bool("release", false, "also want a release tag at HEAD")
	run := flags.Bool("run", false, "also run New as add would call it and check Name() against collage.json")

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
// enforces; with release, that HEAD carries a release tag; with run, that New
// runs as add would call it and Name() matches the manifest.
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
	best := highestReleaseTag(strings.Fields(out.String()))
	if best == "" {
		return "", errors.New("no release tag at HEAD; add rule 5 with git tag vX.Y.Z")
	}
	return best, nil
}

// highestReleaseTag is the highest of tags that is a version and not a
// pseudo-version, or "" when none is.
func highestReleaseTag(tags []string) string {
	best := ""
	for _, t := range tags {
		if !semverTag.MatchString(t) || plugincontract.IsPseudoVersion(t) {
			continue
		}
		if best == "" || compareSemver(t, best) > 0 {
			best = t
		}
	}
	return best
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

// runPluginProgram writes a program into a directory inside dir that calls
// the plugin's New as plugins.go would and checks its Name against the
// manifest, and builds and runs it; the directory is removed afterwards. It neither configures nor starts the plugin: a required field
// an empty config section lacks is the startup error the user then sees, not
// a broken contract.
func (c *CLI) runPluginProgram(ctx context.Context, dir string, p plugincontract.Plugin) error {
	modulePath, err := readModulePath(dir)
	if err != nil {
		return err
	}
	// A -mod=mod in GOFLAGS writes go.sum; a check leaves the repository as
	// it found it.
	snap, err := snapshot(filepath.Join(dir, "go.mod"), filepath.Join(dir, "go.sum"), filepath.Join(dir, "go.work.sum"))
	if err != nil {
		return err
	}
	defer func() {
		if rerr := snap.restore(); rerr != nil {
			fmt.Fprintf(c.stderr(), "collage: plugin check: putting go.mod and go.sum back: %v\n", rerr)
		}
	}()
	// One name, not a random one: the directory is the program's import path,
	// and the go tool keys what it compiles on it, so every run under a new
	// name stored the program in the Go build cache again.
	tmp := filepath.Join(dir, pluginCheckDir)
	if err := os.Mkdir(tmp, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s already exists: another plugin check is running there, or one was interrupted; remove it and check again", tmp)
		}
		return err
	}
	defer os.RemoveAll(tmp)
	src := checkProgram(p.Manifest.Name, modulePath, p.Constructor)
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte(src), 0o644); err != nil {
		return err
	}
	// Built into a file of its own, not with go run, which would also store
	// the linked executable in the build cache.
	out, err := os.MkdirTemp("", "collage-plugin-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(out)
	binary := filepath.Join(out, "check"+exeSuffix())
	var errOut bytes.Buffer
	if err := c.runner().Run(ctx, dir, nil, c.stdout(), &errOut, "go", "build", "-o", binary, "./"+pluginCheckDir); err != nil {
		return goError(errOut.Bytes(), err)
	}
	if err := c.runner().Run(ctx, dir, nil, c.stdout(), &errOut, binary); err != nil {
		return goError(errOut.Bytes(), err)
	}
	return nil
}

// pluginCheckDir is the directory, inside the plugin's repository, that
// plugin check -run writes its program into.
const pluginCheckDir = "collage-plugin-check"

// checkProgram is the source of the program -run builds: it calls New as add
// writes it, in the []collage.Plugin context plugins.go gives the call, and
// checks Name against the manifest.
func checkProgram(name, modulePath string, con plugincontract.Constructor) string {
	q := strconv.Quote(name)
	return `// Code generated by collage plugin check; it is removed once it has run.

package main

import (
	"fmt"
	"os"

	"github.com/Elagoht/collage/pkg/collage"
	plugin ` + strconv.Quote(modulePath) + `
)

func main() {
	var p collage.Plugin = ` + con.Call("plugin") + `
	if p.Name() != ` + q + ` {
		fmt.Fprintf(os.Stderr, "Name() is %q, collage.json says %q\n", p.Name(), ` + q + `)
		os.Exit(1)
	}
	fmt.Println(` + strconv.Quote(name+": New runs, and Name() matches collage.json") + `)
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

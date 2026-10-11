package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

type fakeModule struct {
	Path    string
	Served  string
	Version string
	Files   map[string]string
}

// escapePath is the module proxy's case encoding: an upper-case letter is "!"
// and its lower case, so the path is safe on a case-insensitive file system.
func escapePath(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// fakeProxy writes mods as a GOPROXY=file:// tree and points the go tool at it,
// and at nothing else: no checksum database, a module cache of the test's own.
func fakeProxy(t *testing.T, mods ...fakeModule) {
	t.Helper()
	root := t.TempDir()
	lists := map[string][]string{}
	for _, m := range mods {
		served := m.Served
		if served == "" {
			served = m.Path
		}
		dir := filepath.Join(root, filepath.FromSlash(escapePath(served)), "@v")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{}
		for k, v := range m.Files {
			files[k] = v
		}
		if _, ok := files["go.mod"]; !ok {
			files["go.mod"] = fmt.Sprintf("module %s\n\ngo 1.26\n\nrequire github.com/Elagoht/collage v0.57.0\n", m.Path)
		}
		var zbuf bytes.Buffer
		zw := zip.NewWriter(&zbuf)
		for name, body := range files {
			// A proxy's zip is named for the path it was asked for; the module's
			// own path is the one its go.mod declares.
			w, err := zw.Create(served + "@" + m.Version + "/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		v := escapePath(m.Version)
		for name, body := range map[string][]byte{
			v + ".info": []byte(fmt.Sprintf(`{"Version":%q,"Time":"2026-10-01T00:00:00Z"}`, m.Version)),
			v + ".mod":  []byte(files["go.mod"]),
			v + ".zip":  zbuf.Bytes(),
		} {
			if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		lists[dir] = append(lists[dir], m.Version)
	}
	for dir, versions := range lists {
		if err := os.WriteFile(filepath.Join(dir, "list"), []byte(strings.Join(versions, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(root))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GONOSUMDB", "*")
	t.Setenv("GOFLAGS", "-mod=mod -modcacherw")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOWORK", "off")
}

// fixturePlugin is a minimal plugin module: a collage.json naming owner/name
// (with manifestExtra spliced in as further members, e.g. `"order": 100`), and
// a package whose New is newDecl.
func fixturePlugin(owner, name, version, newDecl, manifestExtra string) fakeModule {
	pkg := strings.ReplaceAll(name, "-", "")
	manifest := fmt.Sprintf(`{"name": %q`, owner+"/"+name)
	if manifestExtra != "" {
		manifest += ", " + manifestExtra
	}
	manifest += "}\n"
	src := fmt.Sprintf(`package %s

import (
	"context"

	"github.com/Elagoht/collage/pkg/collage"
)

type Options struct {
	Greeting string `+"`json:\"greeting\"`"+`
}

type Plugin struct{ opts Options }

%s

func (p *Plugin) Name() string    { return %q }
func (p *Plugin) Version() string { return %q }
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	opts, err := collage.PluginConfig(host, p.opts)
	p.opts = opts
	return err
}
func (p *Plugin) Shutdown(context.Context) error { return nil }
`, pkg, newDecl, owner+"/"+name, strings.TrimPrefix(version, "v"))
	return fakeModule{
		Path:    "github.com/" + owner + "/collage-" + name,
		Version: version,
		Files:   map[string]string{"collage.json": manifest, "plugin.go": src},
	}
}

// repoRootForTest is this repository's root, which a scaffolded project's
// collage is replaced by.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root guess %s has no go.mod: %v", root, err)
	}
	return root
}

// pluginProject serves mods from a fake proxy (see fakeProxy) and scaffolds a
// minimal project whose collage is this repository, at v0.57.0 so the
// fixtures' requirement of it is met by the replacement.
func pluginProject(t *testing.T, mods ...fakeModule) string {
	t.Helper()
	fakeProxy(t, mods...)
	dir := filepath.Join(t.TempDir(), "app")
	c, _, errOut := testCLI()
	if code := c.Run(context.Background(), []string{"new", "app", "--template", "minimal", "-dir", dir, "-module", "pluginapp"}); code != 0 {
		t.Fatalf("new = %d; %s", code, errOut.String())
	}
	for _, args := range [][]string{
		{"mod", "edit", "-replace", "github.com/Elagoht/collage=" + repoRootForTest(t)},
		{"mod", "edit", "-require", "github.com/Elagoht/collage@v0.57.0"},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

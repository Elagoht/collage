package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckModulePath(t *testing.T) {
	for _, ok := range []string{"myapp", "my-app", "my_app", "example.com/demo", "github.com/Elagoht/site.v2", "a~b", "App1"} {
		if err := checkModulePath(ok); err != nil {
			t.Errorf("checkModulePath(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"", ".", "..", "my app", `a"b`, "../esc", "a//b", "/abs", "trail/", ".lead", "trail.", "a/.b", "a/b.", "a/../b",
		"a\\b", "-dash", "ünïcode", "a`b", "a{b", "con", "a/NUL", "a:b", "a\nb",
	} {
		if err := checkModulePath(bad); err == nil {
			t.Errorf("checkModulePath(%q) = nil, want an error", bad)
		}
	}
}

// "collage new ." in an empty directory is the common way to start a project
// where you already are. It used to write "module ." and report success.
func TestRun_New_DotScaffoldsIntoTheCurrentDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "mysite")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	c, out, errOut := testCLI()
	if code := c.Run(context.Background(), []string{"new", "."}); code != 0 {
		t.Fatalf("Run() = %d; %s", code, errOut.String())
	}
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mod), "module mysite\n") {
		t.Errorf("go.mod = %q, want module mysite (the directory's name)", mod)
	}
	if strings.Contains(out.String(), "cd ") {
		t.Errorf("stdout tells you to cd into where you are:\n%s", out.String())
	}
	home, err := os.ReadFile(filepath.Join(dir, "fragments", "pages", "home.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(home), `Name: "mysite"`) {
		t.Errorf("home.go does not carry the derived name:\n%s", home)
	}
}

// A path as the target takes its name from where it points, not from the path
// text: "../esc" is not a module path.
func TestRun_New_PathTargetNamesTheProjectByItsBase(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	c, _, errOut := testCLI()
	if code := c.Run(context.Background(), []string{"new", "../esc"}); code != 0 {
		t.Fatalf("Run() = %d; %s", code, errOut.String())
	}
	mod, err := os.ReadFile(filepath.Join(root, "esc", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mod), "module esc\n") {
		t.Errorf("go.mod = %q, want module esc", mod)
	}
}

// What cannot be a module path is refused before anything is written, with the
// usage exit code.
func TestRun_New_RefusesWhatIsNotAModulePath(t *testing.T) {
	for _, args := range [][]string{
		{"new", "my app"},
		{"new", `a"b`},
		{"new", "site", "--module", "my app"},
		{"new", "site", "--module", "../esc"},
		{"new", "site", "--module", "a//b"},
		{"new", "site", "--dir", "x", "--module", `a"b`},
		{"new", "--dir", "x", "."},
	} {
		work := t.TempDir()
		t.Chdir(work)
		c, _, errOut := testCLI()
		if code := c.Run(context.Background(), args); code != 2 {
			t.Errorf("%v: Run() = %d, want 2; stderr = %s", args, code, errOut.String())
		}
		if !strings.Contains(errOut.String(), "collage: new:") {
			t.Errorf("%v: stderr = %q, want a message from new", args, errOut.String())
		}
		if entries, _ := os.ReadDir(work); len(entries) != 0 {
			t.Errorf("%v: wrote %v before refusing", args, entries)
		}
	}
}

// A display name that is not a module path is fine once --module gives one, but
// it lands in Go source and HTML, so it is quoted and escaped there.
func TestRun_New_DisplayNameIsQuotedAndEscaped(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	target := filepath.Join(work, "out")

	c, _, errOut := testCLI()
	args := []string{"new", `My "<b>" App`, "--template", "demo", "--dir", target, "--module", "example.com/app"}
	if code := c.Run(context.Background(), args); code != 0 {
		t.Fatalf("Run() = %d; %s", code, errOut.String())
	}
	home, err := os.ReadFile(filepath.Join(target, "fragments", "pages", "home.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(home), `Name: "My \"<b>\" App"`) {
		t.Errorf("name not a Go string literal:\n%s", home)
	}
	if !strings.Contains(string(home), `My &#34;&lt;b&gt;&#34; App/`) {
		t.Errorf("name not HTML-escaped:\n%s", home)
	}
}

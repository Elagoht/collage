package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderMan(t *testing.T) {
	page := string(renderMan(&CLI{}, "1.2.3", "2026-10-26"))
	if !strings.HasPrefix(page, `.TH COLLAGE 1 "2026-10-26" "collage 1.2.3"`) {
		t.Errorf("page starts %q", firstLine(page))
	}
	for _, b := range builtins {
		if !strings.Contains(page, "\n.SS "+b.name+"\n") {
			t.Errorf("no section for %s", b.name)
		}
	}
	// Each command's own help is the text, so the page cannot drift from it.
	if !strings.Contains(page, `\-\-template name`) {
		t.Errorf("new's usage text is missing or its dashes are not escaped")
	}
	for _, raw := range []string{"—", "…"} {
		if strings.Contains(page, raw) {
			t.Errorf("page holds a raw %q", raw)
		}
	}
	for i, line := range strings.Split(page, "\n") {
		if strings.HasPrefix(line, "'") {
			t.Errorf("line %d starts with a quote, which roff reads as a request: %q", i+1, line)
		}
	}
}

// mandoc ships with macOS and is what man renders with there.
func TestRenderMan_Lints(t *testing.T) {
	mandoc, err := exec.LookPath("mandoc")
	if err != nil {
		t.Skip("no mandoc")
	}
	for _, date := range []string{"2026-10-26"} {
		cmd := exec.Command(mandoc, "-T", "lint", "-W", "warning")
		cmd.Stdin = bytes.NewReader(renderMan(&CLI{}, "1.2.3", date))
		if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
			t.Errorf("mandoc -T lint, date %q: %v\n%s", date, err, out)
		}
	}
}

func TestRun_Man(t *testing.T) {
	c, out, _ := testCLI()
	// A test binary records no commit date, as one go install builds from the
	// module proxy does not: the page is dated all the same.
	if code := c.Run(t.Context(), []string{"man"}); code != 0 || !strings.HasPrefix(out.String(), ".TH COLLAGE 1 \"2") {
		t.Errorf("man = %d, %q", code, firstLine(out.String()))
	}
}

// --install writes beside the binary's bin directory, in ../share/man/man1,
// which man searches for every directory on PATH.
func TestRun_ManInstall(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(bin, "collage")
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	executable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { executable = os.Executable })

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c, out, errOut := testCLI()
	if code := c.Run(t.Context(), []string{"man", "--install"}); code != 0 {
		t.Fatalf("man --install = %d; %s", code, errOut)
	}
	want := filepath.Join(root, "share", "man", "man1", "collage.1")
	if page, err := os.ReadFile(want); err != nil || !bytes.HasPrefix(page, []byte(".TH COLLAGE 1")) {
		t.Fatalf("%s: %v", want, err)
	}
	if !strings.Contains(out.String(), want) || strings.Contains(out.String(), "PATH") {
		t.Errorf("stdout = %q, want the path and no PATH warning", out)
	}

	t.Setenv("PATH", "/usr/bin")
	c, out, _ = testCLI()
	if code := c.Run(t.Context(), []string{"man", "--install"}); code != 0 || !strings.Contains(out.String(), "PATH") {
		t.Errorf("off PATH: %d, %q; want a note that man will not find it", code, out)
	}
}

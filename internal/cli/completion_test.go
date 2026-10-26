package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Typing "--template=" and TAB, bash hands over "=" as the word: candidates
// keep it, since bash replaces exactly that word.
func TestComplete_BashEqualsAlone(t *testing.T) {
	got, _ := complete([]string{"new", "site", "--template", "="})
	if want := []string{"=minimal", "=demo"}; !slices.Equal(got, want) {
		t.Errorf("complete = %q, want %q", got, want)
	}
}

func TestRun_Completion(t *testing.T) {
	for _, shell := range completionShells {
		c, out, errOut := testCLI()
		if code := c.Run(t.Context(), []string{"completion", shell}); code != 0 || !strings.Contains(out.String(), "__complete") {
			t.Errorf("completion %s = %d; stdout %q stderr %q", shell, code, out, errOut)
		}
	}
	c, _, errOut := testCLI()
	if code := c.Run(t.Context(), []string{"completion", "zhs"}); code != 2 || !strings.Contains(errOut.String(), `did you mean "zsh"`) {
		t.Errorf("completion zhs = %d, %q", code, errOut)
	}
	c, _, _ = testCLI()
	if code := c.Run(t.Context(), []string{"completion"}); code != 2 {
		t.Errorf("completion with no shell = %d, want 2", code)
	}
}

// collageBinary builds this checkout's collage into a temporary bin directory,
// for a shell script to call the way a user's shell would.
func collageBinary(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go binary not available")
	}
	bin := t.TempDir()
	cmd := exec.Command(goBin, "build", "-trimpath", "-o", filepath.Join(bin, "collage"), "../../cmd/collage")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// The bash script runs under macOS's own /bin/bash, which is 3.2: it is given
// the words bash would give it, and what it puts in COMPREPLY is read back.
func TestCompletionScript_Bash(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("no /bin/bash")
	}
	bin := collageBinary(t)
	c, script, _ := testCLI()
	c.Run(t.Context(), []string{"completion", "bash"})
	for _, tc := range []struct {
		words string // as COMP_WORDS, the current one last
		want  string // COMPREPLY joined by spaces
	}{
		{`collage ch`, "check"},
		{`collage add ""`, strings.Join(addKinds, " ")},
		{`collage plugin add @f`, "@forms"},
		{`collage new site --template = de`, "demo"},
		{`collage build -o ""`, "FILES"},
	} {
		t.Run(tc.words, func(t *testing.T) {
			driver := script.String() + `
COMP_WORDS=(` + tc.words + `)
COMP_CWORD=$((${#COMP_WORDS[@]} - 1))
compgen() { echo FILES; }
_collage
echo "${COMPREPLY[*]}"
`
			cmd := exec.Command("/bin/bash", "--norc", "--noprofile", "-c", driver)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("bash: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Errorf("COMPREPLY = %q, want %q", got, tc.want)
			}
		})
	}
}

// zsh and fish cannot be driven from here without a terminal; their scripts
// must at least parse.
func TestCompletionScript_Parses(t *testing.T) {
	for shell, check := range map[string][]string{"zsh": {"-n"}, "fish": {"--no-execute"}} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		c, script, _ := testCLI()
		c.Run(t.Context(), []string{"completion", shell})
		file := filepath.Join(t.TempDir(), "collage."+shell)
		if err := os.WriteFile(file, script.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(path, append(check, file)...).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse its script: %v\n%s", shell, err, out)
		}
	}
}

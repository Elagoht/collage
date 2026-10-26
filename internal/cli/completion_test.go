package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

// The bash script runs under macOS's own /bin/bash, which is 3.2. It reads the
// line as readline has it, COMP_LINE up to COMP_POINT, rather than COMP_WORDS,
// which bash splits at every character of COMP_WORDBREAKS ("=", "@", ":") and
// differently from one version to the next. Readline replaces only the text
// after the last of those characters, so that is what COMPREPLY must hold.
func TestCompletionScript_Bash(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("no /bin/bash")
	}
	bin := collageBinary(t)
	c, script, _ := testCLI()
	c.Run(t.Context(), []string{"completion", "bash"})
	for _, tc := range []struct {
		line string // what is typed, the cursor at its end
		word string // what readline replaces, as bash 3.2 decides it
		want string // COMPREPLY joined by spaces
	}{
		{`collage ch`, "ch", "check"},
		{`collage add `, "", strings.Join(addKinds, " ")},
		{`collage plugin add @f`, "@f", "@forms"},
		{`collage plugin add elagoht/fl`, "elagoht/fl", "elagoht/flash"},
		{`collage new site --template=`, "", "minimal demo"},
		{`collage new site --template=de`, "de", "demo"},
		{`collage new site --template de`, "de", "demo"},
		{`collage new site --with=@forms,@b`, "@b", "@blog"},
		{`collage build -o `, "", "FILES()"},
		{`collage new site --dir=bl`, "bl", "FILES(bl)"},
	} {
		t.Run(tc.line, func(t *testing.T) {
			driver := script.String() + `
COMP_LINE=` + "'" + tc.line + "'" + `
COMP_POINT=${#COMP_LINE}
COMP_WORDS=(placeholder)
COMP_CWORD=0
compgen() { echo "FILES($3)"; }
_collage collage ` + "'" + tc.word + "'" + `
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

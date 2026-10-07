package collage_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The typed keys promise that a wrong type does not compile. This builds a
// program that tries three ways and checks the compiler refuses each.
func TestKeyTypes_WrongTypesDoNotCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}
	out, err := exec.Command("go", "build", "-o", t.TempDir()+"/x", "./testdata/keytypes").CombinedOutput()
	if err == nil {
		t.Fatal("the wrong-type program compiled")
	}
	text := string(out)
	for _, want := range []string{"main.go:15:", "main.go:16:", "main.go:18:"} {
		if !strings.Contains(text, want) {
			t.Errorf("no compile error at %s:\n%s", want, text)
		}
	}
}

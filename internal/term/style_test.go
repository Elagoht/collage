package term

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// FORCE_COLOR is how "collage dev" tells the program it runs that its piped
// stderr ends at a terminal. A file takes it; NO_COLOR still wins over it.
func TestNewStyle_ForceColor(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	if NewStyle(file).Rich() {
		t.Error("a plain file is rich without FORCE_COLOR")
	}

	t.Setenv("FORCE_COLOR", "1")
	if !NewStyle(file).Rich() {
		t.Error("a file is plain with FORCE_COLOR=1")
	}
	if NewStyle(&bytes.Buffer{}).Rich() {
		t.Error("an in-memory writer is rich with FORCE_COLOR=1")
	}

	t.Setenv("FORCE_COLOR", "0")
	if NewStyle(file).Rich() {
		t.Error("a file is rich with FORCE_COLOR=0")
	}

	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "1")
	if NewStyle(file).Rich() {
		t.Error("NO_COLOR did not win over FORCE_COLOR")
	}
}

// Output coloured for a terminal reads the same once its escapes are gone.
func TestStripEscapes(t *testing.T) {
	rich := Style{rich: true}
	coloured := rich.Dim("16:05:23") + " " + rich.Fail("✗") + " build failed" + rich.Dim("  path=/")
	if got, want := StripEscapes(coloured), "16:05:23 ✗ build failed  path=/"; got != want {
		t.Errorf("StripEscapes = %q, want %q", got, want)
	}
	if got := StripEscapes("plain \x1b text"); got != "plain \x1b text" {
		t.Errorf("StripEscapes changed text with no colour codes: %q", got)
	}
}

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAIFiles_Fresh(t *testing.T) {
	dir := t.TempDir()
	got, err := writeAIFiles(dir, "1.2.3")
	if err != nil {
		t.Fatalf("writeAIFiles() error = %v", err)
	}
	if want := (aiWritten{Agents: "created", Claude: "created"}); got != want {
		t.Errorf("writeAIFiles() = %+v, want %+v", got, want)
	}
	if agents := readFile(t, dir, "AGENTS.md"); agents != string(managedSection("1.2.3"))+projectRulesStub {
		t.Errorf("AGENTS.md is not the section and the project-rules stub")
	}
	if claude := readFile(t, dir, "CLAUDE.md"); claude != "@AGENTS.md\n" {
		t.Errorf("CLAUDE.md = %q, want %q", claude, "@AGENTS.md\n")
	}
}

func TestWriteAIFiles_Twice(t *testing.T) {
	dir := t.TempDir()
	if _, err := writeAIFiles(dir, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, dir, "AGENTS.md")
	got, err := writeAIFiles(dir, "1.2.3")
	if err != nil {
		t.Fatalf("writeAIFiles() error = %v", err)
	}
	if want := (aiWritten{Agents: "current", Claude: "exists"}); got != want {
		t.Errorf("writeAIFiles() = %+v, want %+v", got, want)
	}
	if after := readFile(t, dir, "AGENTS.md"); after != before {
		t.Errorf("AGENTS.md changed on a second run")
	}
}

func TestWriteAIFiles_Updates(t *testing.T) {
	dir := t.TempDir()
	if _, err := writeAIFiles(dir, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte(readFile(t, dir, "AGENTS.md")+"- mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := writeAIFiles(dir, "2.0.0")
	if err != nil {
		t.Fatalf("writeAIFiles() error = %v", err)
	}
	if got.Agents != "updated" {
		t.Errorf("Agents = %q, want updated", got.Agents)
	}
	agents := readFile(t, dir, "AGENTS.md")
	if !strings.HasPrefix(agents, "<!-- collage:begin v2.0.0") || !strings.HasSuffix(agents, "- mine\n") {
		t.Errorf("AGENTS.md was not refreshed with the user's rule kept:\n%s", agents)
	}
}

func TestWriteAIFiles_KeepsClaude(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := writeAIFiles(dir, "1.2.3")
	if err != nil {
		t.Fatalf("writeAIFiles() error = %v", err)
	}
	if got.Claude != "exists" {
		t.Errorf("Claude = %q, want exists", got.Claude)
	}
	if claude := readFile(t, dir, "CLAUDE.md"); claude != "mine\n" {
		t.Errorf("CLAUDE.md = %q, want it untouched", claude)
	}
}

func TestWriteAIFiles_BadMarkers(t *testing.T) {
	dir := t.TempDir()
	bad := "<!-- collage:begin v1 -->\nno end\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeAIFiles(dir, "1.2.3"); !errors.Is(err, ErrAgentsMarkers) {
		t.Fatalf("writeAIFiles() error = %v, want ErrAgentsMarkers", err)
	}
	if agents := readFile(t, dir, "AGENTS.md"); agents != bad {
		t.Errorf("AGENTS.md changed after a refusal")
	}
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("CLAUDE.md written after a refusal")
	}
}

// newProject scaffolds a minimal project into a temp directory, without --ai.
func newProject(t *testing.T) string {
	t.Helper()
	c, _, errOut := testCLI()
	dir := filepath.Join(t.TempDir(), "proj")
	if code := c.Run(context.Background(), []string{"new", "site", "-dir", dir}); code != 0 {
		t.Fatalf("new = %d; stderr = %s", code, errOut)
	}
	return dir
}

func TestRun_AI_Init(t *testing.T) {
	dir := newProject(t)
	c, out, errOut := testCLI()
	if code := c.Run(context.Background(), []string{"ai", "init", "-dir", dir}); code != 0 {
		t.Fatalf("ai init = %d, want 0; stderr = %s", code, errOut)
	}
	for _, want := range []string{"AGENTS.md: created", "CLAUDE.md: created"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout = %q, want it to say %q", out, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Errorf("AGENTS.md: %v", err)
	}
}

func TestRun_AI_NotAProject(t *testing.T) {
	c, _, errOut := testCLI()
	if code := c.Run(context.Background(), []string{"ai", "init", "-dir", t.TempDir()}); code != 1 {
		t.Fatalf("ai init = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "go.mod") {
		t.Errorf("stderr = %q, want it to name go.mod", errOut)
	}
}

func TestRun_AI_NoSubcommand(t *testing.T) {
	c, _, errOut := testCLI()
	if code := c.Run(context.Background(), []string{"ai"}); code != 2 {
		t.Fatalf("ai = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "collage ai init") {
		t.Errorf("stderr = %q, want the usage", errOut)
	}
	c, out, _ := testCLI()
	if code := c.Run(context.Background(), []string{"help", "ai"}); code != 0 || !strings.Contains(out.String(), "collage ai init") {
		t.Errorf("help ai = %d, %q; want 0 and the usage", code, out)
	}
}

func TestRun_AI_ClaudeHint(t *testing.T) {
	dir := newProject(t)
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, out, errOut := testCLI()
	if code := c.Run(context.Background(), []string{"ai", "init", "-dir", dir}); code != 0 {
		t.Fatalf("ai init = %d; stderr = %s", code, errOut)
	}
	if !strings.Contains(out.String(), `add a line "@AGENTS.md" to it`) {
		t.Errorf("stdout = %q, want the CLAUDE.md hint", out)
	}
}

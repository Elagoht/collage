package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingRunner remembers the command it was asked to run instead of running it.
type recordingRunner struct {
	env  []string
	args []string
	name string
}

func (r *recordingRunner) Run(_ context.Context, _ string, env []string, _, _ io.Writer, name string, args ...string) error {
	r.env, r.name, r.args = env, name, args
	return nil
}

func inProject(t *testing.T, module string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+module+"\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
}

func runBuildIn(t *testing.T, args ...string) (*recordingRunner, string) {
	t.Helper()
	runner := &recordingRunner{}
	var out bytes.Buffer
	cli := &CLI{Stdout: &out, Stderr: &out, Runner: runner}
	if code := cli.runBuild(context.Background(), args); code != 0 {
		t.Fatalf("build exited %d: %s", code, out.String())
	}
	return runner, out.String()
}

// The default target is not this machine. A binary built for a Mac does not run in
// a Linux container, and "exec format error" on a server is the wrong place to find
// that out.
func TestBuild_DefaultsToLinuxAmd64(t *testing.T) {
	inProject(t, "example.com/shop")
	runner, _ := runBuildIn(t)

	want := map[string]bool{"CGO_ENABLED=0": true, "GOOS=linux": true, "GOARCH=amd64": true}
	for _, entry := range runner.env {
		delete(want, entry)
	}
	if len(want) != 0 {
		t.Errorf("env = %v, missing %v", runner.env, want)
	}
}

// The flags are the point of the command: they are what somebody would otherwise
// have to remember, every time, correctly.
func TestBuild_UsesTheFlagsThatMatter(t *testing.T) {
	inProject(t, "example.com/shop")
	runner, _ := runBuildIn(t)

	joined := strings.Join(runner.args, " ")
	for _, want := range []string{"build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join("bin", "shop")} {
		if !strings.Contains(joined, want) {
			t.Errorf("args = %v, want them to contain %q", runner.args, want)
		}
	}
}

// bin/, not dist/: "collage export" writes a static site to dist/ and removes that
// directory's contents with -clean, which would delete a binary sitting in it.
func TestBuild_WritesBesideTheStaticSiteNotIntoIt(t *testing.T) {
	inProject(t, "example.com/shop")
	runner, _ := runBuildIn(t)

	joined := strings.Join(runner.args, " ")
	if strings.Contains(joined, filepath.Join("dist", "shop")) {
		t.Errorf("args = %v: the binary would be written where export -clean deletes", runner.args)
	}
}

func TestBuild_HonoursItsTarget(t *testing.T) {
	inProject(t, "example.com/shop")
	runner, _ := runBuildIn(t, "-os", "darwin", "-arch", "arm64", "-o", "out/thing")

	joined := strings.Join(runner.env, " ") + " " + strings.Join(runner.args, " ")
	for _, want := range []string{"GOOS=darwin", "GOARCH=arm64", "out/thing"} {
		if !strings.Contains(joined, want) {
			t.Errorf("build for darwin/arm64 = %q, want it to contain %q", joined, want)
		}
	}
}

// A Windows binary is named like one, because a file without .exe is a file Windows
// will not run.
func TestBuild_WindowsGetsAnExtension(t *testing.T) {
	inProject(t, "example.com/shop")
	runner, _ := runBuildIn(t, "-os", "windows")

	if !strings.Contains(strings.Join(runner.args, " "), "shop.exe") {
		t.Errorf("args = %v, want the binary named shop.exe", runner.args)
	}
}

// Without -i, nothing but the binary is produced. Writing files somebody did not
// ask for is how a build command earns a reputation.
func TestBuild_WritesNoExtraFilesWithoutInteractive(t *testing.T) {
	inProject(t, "example.com/shop")
	runBuildIn(t)

	for _, path := range []string{"Dockerfile", filepath.Join("bin", "Dockerfile"), filepath.Join("bin", "shop.service")} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s was written without -i", path)
		}
	}
}

func TestBuild_InteractiveWritesWhatWasAskedFor(t *testing.T) {
	inProject(t, "example.com/shop")

	runner := &recordingRunner{}
	var out bytes.Buffer
	cli := &CLI{Stdout: &out, Stderr: &out, Runner: runner, Stdin: strings.NewReader("y\nn\n")}
	if code := cli.runBuild(context.Background(), []string{"-i"}); code != 0 {
		t.Fatalf("build exited %d: %s", code, out.String())
	}

	// Beside the binary, not at the project root: they are generated, and the
	// root is for what a person wrote.
	if _, err := os.Stat(filepath.Join("bin", "Dockerfile")); err != nil {
		t.Errorf("bin/Dockerfile was not written after answering yes: %v", err)
	}
	if _, err := os.Stat("Dockerfile"); err == nil {
		t.Error("a Dockerfile was written at the project root")
	}
	if _, err := os.Stat(filepath.Join("bin", "shop.service")); err == nil {
		t.Error("a systemd unit was written after answering no")
	}
	// And the one thing that costs is said rather than left to be worked out.
	if !strings.Contains(out.String(), "docker build -f bin/Dockerfile .") {
		t.Errorf("output = %q, want it to name the command that uses the generated file", out.String())
	}
}

// An existing Dockerfile is something a project edited. A build command that
// replaces it with a default is one that quietly undoes somebody's work.
func TestBuild_NeverOverwritesAnExistingFile(t *testing.T) {
	inProject(t, "example.com/shop")
	if err := os.MkdirAll("bin", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("bin", "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{}
	var out bytes.Buffer
	cli := &CLI{Stdout: &out, Stderr: &out, Runner: runner, Stdin: strings.NewReader("y\nn\n")}
	cli.runBuild(context.Background(), []string{"-i"})

	kept, err := os.ReadFile(filepath.Join("bin", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "FROM scratch\n" {
		t.Errorf("Dockerfile = %q, want the one that was already there", kept)
	}
	if !strings.Contains(out.String(), "already exists") {
		t.Errorf("output = %q, want it to say the file was left alone", out.String())
	}
}

// Outside a Go project there is nothing to build, and saying so beats failing
// inside the toolchain with a message about a package pattern.
func TestBuild_OutsideAProject(t *testing.T) {
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	cli := &CLI{Stdout: &out, Stderr: &out, Runner: &recordingRunner{}}
	if code := cli.runBuild(context.Background(), nil); code == 0 {
		t.Fatal("build succeeded with no go.mod")
	}
	if !strings.Contains(out.String(), "go.mod") {
		t.Errorf("output = %q, want it to mention go.mod", out.String())
	}
}

package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRun_Build_DefaultOut(t *testing.T) {
	c, _, _ := testCLI()
	runner := &fakeRunner{}
	c.Runner = runner

	code := c.Run(context.Background(), []string{"build"})

	if code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
	if runner.callCount != 1 {
		t.Fatalf("runner called %d times, want 1", runner.callCount)
	}
	if runner.name != "go" {
		t.Errorf("name = %q, want %q", runner.name, "go")
	}
	want := []string{"run", ".", "-collage-build", "-out", "dist"}
	if got := strings.Join(runner.args, " "); got != strings.Join(want, " ") {
		t.Errorf("args = %q, want %q", got, strings.Join(want, " "))
	}
	if len(runner.env) != 0 {
		t.Errorf("env = %v, want empty (build does not set COLLAGE_DEV)", runner.env)
	}
}

func TestRun_Build_CustomOut(t *testing.T) {
	c, _, _ := testCLI()
	runner := &fakeRunner{}
	c.Runner = runner

	code := c.Run(context.Background(), []string{"build", "-out", "public"})

	if code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
	want := []string{"run", ".", "-collage-build", "-out", "public"}
	if got := strings.Join(runner.args, " "); got != strings.Join(want, " ") {
		t.Errorf("args = %q, want %q", got, strings.Join(want, " "))
	}
}

func TestRun_Build_RunnerErrorExitsOne(t *testing.T) {
	c, _, errOut := testCLI()
	c.Runner = &fakeRunner{err: errors.New("exit status 1")}

	code := c.Run(context.Background(), []string{"build"})

	if code != 1 {
		t.Fatalf("Run() = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "exit status 1") {
		t.Errorf("stderr = %q, want it to contain the runner's error", errOut.String())
	}
}

func TestRun_Build_UnknownFlagExitsTwo(t *testing.T) {
	c, _, errOut := testCLI()
	c.Runner = &fakeRunner{}

	code := c.Run(context.Background(), []string{"build", "-nosuchflag"})

	if code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}
	if errOut.Len() == 0 {
		t.Error("stderr is empty, want a flag-parsing error")
	}
}

func TestRun_Build_RejectsPositionalArgs(t *testing.T) {
	c, _, errOut := testCLI()
	c.Runner = &fakeRunner{}

	code := c.Run(context.Background(), []string{"build", "extra"})

	if code != 2 {
		t.Fatalf("Run() = %d, want 2", code)
	}
	if errOut.Len() == 0 {
		t.Error("stderr is empty, want a usage message")
	}
}

func TestRun_Build_NeverStartsARealProcess(t *testing.T) {
	// The default Runner (nil, so execRunner) would try to run a real "go"
	// binary; every build test above supplies a fakeRunner instead. This test
	// exists to document that requirement, since it is easy to reintroduce
	// silently by forgetting to set CLI.Runner in a future test.
	c, _, _ := testCLI()
	runner := &fakeRunner{}
	c.Runner = runner

	c.Run(context.Background(), []string{"build"})

	if runner.callCount == 0 {
		t.Fatal("fakeRunner was never called; build must go through CLI.Runner")
	}
}

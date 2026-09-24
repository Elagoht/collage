package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer the dev loop can write to while a test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type devCall struct {
	name string
	args []string
	env  []string
}

// devRunner stands in for the go tool and for the built program: a build
// succeeds unless failBuild is set, and a program runs until it is stopped.
type devRunner struct {
	mu        sync.Mutex
	builds    []devCall
	failBuild atomic.Bool
	started   chan devCall
	stopped   atomic.Int32
}

func newDevRunner() *devRunner {
	return &devRunner{started: make(chan devCall, 16)}
}

func (r *devRunner) Run(ctx context.Context, _ string, env []string, _, _ io.Writer, name string, args ...string) error {
	call := devCall{name: name, args: append([]string(nil), args...), env: append([]string(nil), env...)}
	if name == "go" {
		r.mu.Lock()
		r.builds = append(r.builds, call)
		r.mu.Unlock()
		if r.failBuild.Load() {
			return errors.New("exit status 1")
		}
		return nil
	}
	r.started <- call
	<-ctx.Done()
	r.stopped.Add(1)
	return ctx.Err()
}

func (r *devRunner) buildCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.builds)
}

// devSession runs "collage dev" in a fresh project directory until the test
// ends, with the watcher polling fast enough for a test.
func devSession(t *testing.T, files map[string]string) (*devRunner, *syncBuffer, func() int) {
	t.Helper()
	previous := watchInterval
	watchInterval = 20 * time.Millisecond
	t.Cleanup(func() { watchInterval = previous })

	dir := t.TempDir()
	for name, content := range files {
		writeProjectFile(t, filepath.Join(dir, name), content)
	}
	t.Chdir(dir)

	runner := newDevRunner()
	stderr := &syncBuffer{}
	c := &CLI{Stdout: &syncBuffer{}, Stderr: stderr, Runner: runner}

	ctx, cancel := context.WithCancel(context.Background())
	code := make(chan int, 1)
	go func() { code <- c.Run(ctx, []string{"dev"}) }()

	stop := func() int {
		cancel()
		select {
		case got := <-code:
			return got
		case <-time.After(5 * time.Second):
			t.Fatal("collage dev did not stop")
			return -1
		}
	}
	t.Cleanup(func() { cancel() })
	return runner, stderr, stop
}

// writeProjectFile writes content at path, and moves its modification time on,
// so two writes in the same clock tick are still two different stamps.
func writeProjectFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Duration(len(content)+1) * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

func waitStarted(t *testing.T, runner *devRunner) devCall {
	t.Helper()
	select {
	case call := <-runner.started:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("the program was never started")
		return devCall{}
	}
}

func expectNoStart(t *testing.T, runner *devRunner, within time.Duration) {
	t.Helper()
	select {
	case call := <-runner.started:
		t.Fatalf("the program was started again (%s), want it left alone", call.name)
	case <-time.After(within):
	}
}

// It builds the program, runs what it built with COLLAGE_DEV=1 last, and stops
// it when the session ends.
func TestDev_BuildsAndRunsTheProgram(t *testing.T) {
	runner, _, stop := devSession(t, map[string]string{"main.go": "package main"})

	call := waitStarted(t, runner)
	if runner.buildCount() != 1 {
		t.Fatalf("builds = %d, want 1", runner.buildCount())
	}
	build := runner.builds[0]
	if build.args[0] != "build" || build.args[1] != "-o" || build.args[3] != "." {
		t.Errorf("build = go %s, want go build -o <binary> .", strings.Join(build.args, " "))
	}
	if call.name != build.args[2] {
		t.Errorf("ran %q, want the binary the build wrote, %q", call.name, build.args[2])
	}
	if len(call.env) == 0 || call.env[len(call.env)-1] != "COLLAGE_DEV=1" {
		t.Errorf("env = %v, want COLLAGE_DEV=1 last", call.env)
	}

	if code := stop(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if runner.stopped.Load() != 1 {
		t.Errorf("stopped = %d, want the program stopped with the session", runner.stopped.Load())
	}
}

// A change to Go code rebuilds and restarts. A change to anything the running
// program reads from disk, or writes itself, does not.
func TestDev_RebuildsOnlyForWhatTheProgramIsMadeOf(t *testing.T) {
	runner, _, stop := devSession(t, map[string]string{"main.go": "package main"})
	waitStarted(t, runner)

	for _, name := range []string{
		"templates/pages/home.html",
		"static/app.css",
		".cache/abc/entry",
		"dist/index.html",
		"main_test.go",
		"bin/app",
	} {
		writeProjectFile(t, name, "changed "+name)
	}
	expectNoStart(t, runner, 200*time.Millisecond)
	if runner.buildCount() != 1 {
		t.Fatalf("builds = %d after non-source changes, want 1", runner.buildCount())
	}

	writeProjectFile(t, "pages/home.go", "package pages")
	waitStarted(t, runner)
	if runner.buildCount() != 2 {
		t.Errorf("builds = %d, want 2", runner.buildCount())
	}
	if runner.stopped.Load() != 1 {
		t.Errorf("stopped = %d, want the old program stopped before the new one ran", runner.stopped.Load())
	}
	stop()
}

// A change that does not compile leaves the last good build serving.
func TestDev_AFailedBuildKeepsTheLastGoodOne(t *testing.T) {
	runner, stderr, stop := devSession(t, map[string]string{"main.go": "package main"})
	waitStarted(t, runner)

	runner.failBuild.Store(true)
	writeProjectFile(t, "main.go", "package main // broken")
	deadline := time.Now().Add(5 * time.Second)
	for runner.buildCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	expectNoStart(t, runner, 100*time.Millisecond)
	if runner.stopped.Load() != 0 {
		t.Error("the running program was stopped for a build that failed")
	}
	if !strings.Contains(stderr.String(), "last good build is still serving") {
		t.Errorf("stderr = %q, want it to say the last build is still serving", stderr.String())
	}

	runner.failBuild.Store(false)
	writeProjectFile(t, "main.go", "package main // fixed")
	waitStarted(t, runner)
	stop()
}

// The environment file is read on every restart, and a change to it restarts.
func TestDev_TheEnvFileIsReadAndWatched(t *testing.T) {
	runner, stderr, stop := devSession(t, map[string]string{
		"main.go":          "package main",
		".env.development": "COLLAGE_TEST_KEY=one\n",
	})

	if call := waitStarted(t, runner); call.env[0] != "COLLAGE_TEST_KEY=one" {
		t.Errorf("env = %v, want the file's variable", call.env)
	}
	if !strings.Contains(stderr.String(), "loaded .env.development") {
		t.Errorf("stderr = %q, want it to name the file", stderr.String())
	}

	writeProjectFile(t, ".env.development", "COLLAGE_TEST_KEY=two\n")
	if call := waitStarted(t, runner); call.env[0] != "COLLAGE_TEST_KEY=two" {
		t.Errorf("env = %v, want the edited value", call.env)
	}
	stop()
}

// A malformed environment file is reported with its line, and nothing runs
// until it is fixed.
func TestDev_AMalformedEnvFileRunsNothing(t *testing.T) {
	runner, stderr, stop := devSession(t, map[string]string{
		"main.go": "package main",
		".env":    "PORT 3000\n",
	})

	expectNoStart(t, runner, 150*time.Millisecond)
	if !strings.Contains(stderr.String(), ".env:1") {
		t.Errorf("stderr = %q, want it to name .env:1", stderr.String())
	}

	writeProjectFile(t, ".env", "PORT=3000\n")
	waitStarted(t, runner)
	stop()
}

func TestWatchedFile(t *testing.T) {
	for rel, want := range map[string]bool{
		"main.go":                   true,
		"pages/home.go":             true,
		"pages/home_test.go":        false,
		"go.mod":                    true,
		"go.sum":                    true,
		"tools/go.mod":              false,
		".env":                      true,
		".env.development":          true,
		"pages/.env":                false,
		"templates/pages/home.html": false,
		"static/app.css":            false,
		"plugins-config.json":       false,
	} {
		if got := watchedFile(rel); got != want {
			t.Errorf("watchedFile(%q) = %v, want %v", rel, got, want)
		}
	}
}

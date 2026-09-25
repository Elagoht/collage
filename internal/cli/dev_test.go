package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
// succeeds unless failBuild is set, printing buildOutput when it fails, and a
// program runs program when it is set and until it is stopped when it is not.
type devRunner struct {
	mu          sync.Mutex
	builds      []devCall
	failBuild   atomic.Bool
	buildOutput string
	program     func(ctx context.Context, env []string, stderr io.Writer) error
	started     chan devCall
	stopped     atomic.Int32
}

func newDevRunner() *devRunner {
	return &devRunner{started: make(chan devCall, 16)}
}

func (r *devRunner) Run(ctx context.Context, _ string, env []string, _, stderr io.Writer, name string, args ...string) error {
	call := devCall{name: name, args: append([]string(nil), args...), env: append([]string(nil), env...)}
	if name == "go" {
		r.mu.Lock()
		r.builds = append(r.builds, call)
		output := r.buildOutput
		r.mu.Unlock()
		if r.failBuild.Load() {
			fmt.Fprint(stderr, output)
			return errors.New("exit status 1")
		}
		return nil
	}
	r.started <- call
	r.mu.Lock()
	program := r.program
	r.mu.Unlock()
	if program != nil {
		err := program(ctx, env, stderr)
		r.stopped.Add(1)
		return err
	}
	<-ctx.Done()
	r.stopped.Add(1)
	return ctx.Err()
}

func (r *devRunner) setProgram(program func(ctx context.Context, env []string, stderr io.Writer) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.program = program
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
	return devSessionWith(t, files, newDevRunner())
}

// devSessionWith is devSession with a runner the test has already set up, so
// the first program it starts already behaves as the test wants.
func devSessionWith(t *testing.T, files map[string]string, runner *devRunner) (*devRunner, *syncBuffer, func() int) {
	t.Helper()
	// A port of its own, so no session collides with another, or with a
	// server the machine running the tests already has on 3000.
	t.Setenv("PORT", strconv.Itoa(freePort(t)))
	t.Setenv("HOST", "127.0.0.1")
	previous := watchInterval
	watchInterval = 20 * time.Millisecond
	t.Cleanup(func() { watchInterval = previous })

	dir := t.TempDir()
	for name, content := range files {
		writeProjectFile(t, filepath.Join(dir, name), content)
	}
	t.Chdir(dir)

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

// freePort is a port nothing on this machine is listening on right now.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// envValue is the value the last KEY=value in env gives key, which is the one
// the started process sees.
func envValue(env []string, key string) string {
	var value string
	for _, pair := range env {
		if k, v, ok := strings.Cut(pair, "="); ok && k == key {
			value = v
		}
	}
	return value
}

// serveProgram is a program that listens where it is told to, after delay,
// and answers every request with handler until it is stopped.
func serveProgram(delay time.Duration, handler http.HandlerFunc) func(context.Context, []string, io.Writer) error {
	return func(ctx context.Context, env []string, _ io.Writer) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		listener, err := net.Listen("tcp", net.JoinHostPort(envValue(env, "HOST"), envValue(env, "PORT")))
		if err != nil {
			return err
		}
		server := &http.Server{Handler: handler}
		go server.Serve(listener)
		<-ctx.Done()
		server.Close()
		return ctx.Err()
	}
}

// crashProgram is a program that prints output and exits with an error before
// it listens, the way one whose pages fail to register does.
func crashProgram(output string) func(context.Context, []string, io.Writer) error {
	return func(_ context.Context, _ []string, stderr io.Writer) error {
		fmt.Fprint(stderr, output)
		return errors.New("exit status 1")
	}
}

// devGet requests path from the session's own address, retrying while it is not
// listening yet.
func devGet(t *testing.T, path string) (int, string) {
	t.Helper()
	url := "http://" + net.JoinHostPort(os.Getenv("HOST"), os.Getenv("PORT")) + path
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := http.Get(url)
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			return response.StatusCode, string(body)
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s: %v", path, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The browser talks to collage dev, which passes each request on to the
// program with the Host it was sent to, and the program is told to listen
// somewhere else.
func TestDev_ProxiesToTheProgram(t *testing.T) {
	runner := newDevRunner()
	runner.setProgram(serveProgram(0, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "app %s %s", r.Host, r.URL.Path)
	}))
	_, _, stop := devSessionWith(t, map[string]string{"main.go": "package main"}, runner)

	call := waitStarted(t, runner)
	if envValue(call.env, "PORT") == os.Getenv("PORT") {
		t.Errorf("the program was told PORT=%s, the port collage dev listens on", os.Getenv("PORT"))
	}

	status, body := devGet(t, "/recipes/soup")
	want := "app " + net.JoinHostPort(os.Getenv("HOST"), os.Getenv("PORT")) + " /recipes/soup"
	if status != http.StatusOK || body != want {
		t.Errorf("GET = %d %q, want 200 %q", status, body, want)
	}
	stop()
}

// A request that arrives while the program is starting waits for it to listen,
// rather than failing the way a refused connection would.
func TestDev_ARequestWaitsForTheProgramToListen(t *testing.T) {
	runner := newDevRunner()
	runner.setProgram(serveProgram(300*time.Millisecond, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "ready")
	}))
	_, _, stop := devSessionWith(t, map[string]string{"main.go": "package main"}, runner)

	if status, body := devGet(t, "/"); status != http.StatusOK || body != "ready" {
		t.Errorf("GET = %d %q, want 200 \"ready\"", status, body)
	}
	stop()
}

// A program that exits by itself leaves its output in the browser, not only in
// the terminal — escaped, and with the reload script, so the page replaces
// itself once a change brings the program back.
func TestDev_ACrashIsShownInTheBrowser(t *testing.T) {
	runner := newDevRunner()
	runner.setProgram(crashProgram(`cookbook: register page "recipe": collage: template not found: <fradgments/more-recipes.html>` + "\n"))
	_, _, stop := devSessionWith(t, map[string]string{"main.go": "package main"}, runner)

	status, body := devGet(t, "/")
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", status)
	}
	for _, want := range []string{
		`template not found: &lt;fradgments/more-recipes.html&gt;`,
		`exit status 1`,
		`/_collage/reload`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not contain %q:\n%s", want, body)
		}
	}

	runner.setProgram(serveProgram(0, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "fixed")
	}))
	writeProjectFile(t, "pages/recipe.go", "package pages // fixed")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if status, body := devGet(t, "/"); status == http.StatusOK && body == "fixed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fixed program was never served")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
}

// A first build that fails has nothing to serve, so its output is the page.
func TestDev_AFailedFirstBuildIsShownInTheBrowser(t *testing.T) {
	runner := newDevRunner()
	runner.buildOutput = "./main.go:3:2: undefined: recipes\n"
	runner.failBuild.Store(true)
	_, _, stop := devSessionWith(t, map[string]string{"main.go": "package main"}, runner)

	status, body := devGet(t, "/")
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "undefined: recipes") {
		t.Errorf("GET = %d, want 503 with the compiler's output:\n%s", status, body)
	}
	stop()
}

// A page left open when the program goes down is told to reload by the same
// stream a running program serves: it names a different instance, and it ends
// when the program is back, so the page reconnects to the program itself.
func TestDev_TheReloadStreamFollowsTheProgram(t *testing.T) {
	runner := newDevRunner()
	runner.setProgram(crashProgram("boom\n"))
	_, _, stop := devSessionWith(t, map[string]string{"main.go": "package main"}, runner)
	devGet(t, "/") // until it is down and answering

	url := "http://" + net.JoinHostPort(os.Getenv("HOST"), os.Getenv("PORT")) + "/_collage/reload"
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	first := make([]byte, 256)
	n, _ := response.Body.Read(first)
	if !strings.Contains(string(first[:n]), "event: hello") {
		t.Fatalf("stream began %q, want a hello event", first[:n])
	}

	ended := make(chan struct{})
	go func() {
		io.Copy(io.Discard, response.Body)
		close(ended)
	}()
	select {
	case <-ended:
		t.Fatal("the stream ended while the program was still down")
	case <-time.After(150 * time.Millisecond):
	}

	runner.setProgram(serveProgram(0, func(w http.ResponseWriter, _ *http.Request) {}))
	writeProjectFile(t, "main.go", "package main // fixed")
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not end when the program came back")
	}
	stop()
}

// A program that runs and never listens where it was told — a main.go that
// ignores HOST and PORT — is named on the page rather than left to hang the
// browser forever.
func TestDev_AProgramThatNeverListensIsReported(t *testing.T) {
	previous := devListenTimeout
	devListenTimeout = 200 * time.Millisecond
	t.Cleanup(func() { devListenTimeout = previous })

	runner := newDevRunner() // runs until stopped, listening nowhere
	_, _, stop := devSessionWith(t, map[string]string{"main.go": "package main"}, runner)
	call := waitStarted(t, runner)

	status, body := devGet(t, "/")
	target := net.JoinHostPort(envValue(call.env, "HOST"), envValue(call.env, "PORT"))
	if status != http.StatusServiceUnavailable || !strings.Contains(body, target) {
		t.Errorf("GET = %d, want 503 naming %s:\n%s", status, target, body)
	}
	stop()
}

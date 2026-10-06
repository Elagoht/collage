package collage_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// These hold the framework's promises under many requests at once: one render for
// a crowd waiting on the same page, a disk cache that stays inside its cap however
// many keys a caller invents, nothing left running after a request is abandoned or
// a handler panics, and a shutdown that lets the requests in flight finish. Each
// one is a property the functional tests pin for a single request.

func stressApp(t *testing.T, cache collage.CacheConfig) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "127.0.0.1", Port: 0},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/keep.html": {Data: []byte(`x`)}}, Root: "t"},
		Cache:    cache,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

var memoryCache = collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour}

func skipShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("stress test")
	}
}

// crowd sends n requests at once, released together, and returns their answers.
func crowd(h http.Handler, n int, req func(i int) *http.Request) []*httptest.ResponseRecorder {
	out := make([]*httptest.ResponseRecorder, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			r := req(i)
			<-start
			out[i] = httptest.NewRecorder()
			h.ServeHTTP(out[i], r)
		})
	}
	close(start)
	wg.Wait()
	return out
}

// settled waits for the goroutine count to come back to what it was, since a
// goroutine that finished a moment ago may not have exited yet, and fails with
// the stacks of what is still running if it does not.
func settled(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		now := runtime.NumGoroutine()
		if now <= before {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("%d goroutines still running, %d before:\n%s", now, before, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStress_ACrowdOnAColdPageRendersItOnce(t *testing.T) {
	skipShort(t)
	app := stressApp(t, memoryCache)
	var renders atomic.Int32
	slow := collage.NewInlineFragment("slow", `<p>{{.}}</p>`).WithData(collage.Load(
		func(context.Context, *collage.RenderContext) (string, error) {
			renders.Add(1)
			time.Sleep(50 * time.Millisecond)
			return "rendered", nil
		})).
		Build()
	if err := app.RegisterPage(collage.NewPage("p").WithContent(slow).WithPath("en", "/").Incremental(time.Hour).Build()); err != nil {
		t.Fatal(err)
	}
	for i, rec := range crowd(app.Handler(), 200, func(int) *http.Request { return httptest.NewRequest(http.MethodGet, "/", nil) }) {
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<p>rendered</p>") {
			t.Fatalf("request %d: %d %q", i, rec.Code, rec.Body.String())
		}
	}
	if n := renders.Load(); n != 1 {
		t.Errorf("200 requests for a cold page rendered it %d times, want 1", n)
	}
}

// A render that fails fails for everyone waiting on it, and is not remembered: the
// next request tries again rather than finding the crowd's render stuck.
func TestStress_AFailedSharedRenderIsTriedAgain(t *testing.T) {
	skipShort(t)
	app := stressApp(t, memoryCache)
	var renders atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	failing := collage.NewInlineFragment("failing", `<p>{{.}}</p>`).Required().WithData(collage.Load(
		func(context.Context, *collage.RenderContext) (string, error) {
			if renders.Add(1) == 1 {
				close(entered)
				<-release
				return "", errors.New("upstream down")
			}
			return "recovered", nil
		})).
		Build()
	if err := app.RegisterPage(collage.NewPage("p").WithContent(failing).WithPath("en", "/").Incremental(time.Hour).Build()); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()
	// The render fails only once the crowd has had time to join it; a request
	// arriving after it failed would start a render of its own, which is the
	// retry this test expects of the next request, not of the crowd.
	go func() {
		<-entered
		time.Sleep(300 * time.Millisecond)
		close(release)
	}()
	for i, rec := range crowd(h, 100, func(int) *http.Request { return httptest.NewRequest(http.MethodGet, "/", nil) }) {
		if rec.Code == http.StatusOK {
			t.Fatalf("request %d of the failed render answered 200: %q", i, rec.Body.String())
		}
	}
	if n := renders.Load(); n != 1 {
		t.Errorf("the crowd rendered the page %d times, want 1", n)
	}
	rec := nextjsGet(h, "/", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "recovered") {
		t.Errorf("the next request: %d %q", rec.Code, rec.Body.String())
	}
}

func cacheFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A caller who invents a new query for every request writes a new entry with
// each one; the disk holds no more of them than the cap, however many arrive at
// once.
func TestStress_TheDiskCacheStaysInsideItsCap(t *testing.T) {
	skipShort(t)
	const limit = 50
	dir := t.TempDir()
	app := stressApp(t, collage.CacheConfig{Enabled: true, Type: "disk", Dir: dir, DefaultTTL: time.Hour, MaxEntries: limit})
	if err := app.RegisterPage(collage.NewPage("p").WithContent(greeting("g")).WithPath("en", "/").
		Incremental(time.Hour).WithCacheParams("n").Build()); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()
	for round := range 3 {
		for i, rec := range crowd(h, 2*limit, func(i int) *http.Request {
			return httptest.NewRequest(http.MethodGet, "/?n="+strconv.Itoa(round*1000+i), nil)
		}) {
			if rec.Code != http.StatusOK {
				t.Fatalf("round %d request %d: %d", round, i, rec.Code)
			}
		}
		if n := cacheFiles(t, dir); n > limit {
			t.Fatalf("after round %d the disk holds %d entries, cap %d", round, n, limit)
		}
	}
}

// A reader who leaves, a part that runs out of time, a handler that panics: none
// of them leaves anything running once its request is answered.
func TestStress_AbandonedAndFailedRequestsLeaveNothingRunning(t *testing.T) {
	skipShort(t)
	app := stressApp(t, collage.CacheConfig{})
	waits := collage.NewInlineFragment("waits", `<p>{{.}}</p>`).WithData(collage.Load(
		func(ctx context.Context, _ *collage.RenderContext) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})).
		Build()
	late := collage.NewInlineFragment("late", `<p>{{.}}</p>`).WithTimeout(10 * time.Millisecond).WithData(collage.Load(
		func(ctx context.Context, _ *collage.RenderContext) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})).
		Build()
	panics := collage.NewInlineFragment("panics", `<p>{{.}}</p>`).WithData(collage.Load(
		func(context.Context, *collage.RenderContext) (string, error) {
			panic("handler bug")
		})).
		Build()
	for path, content := range map[string]*collage.Fragment{"/waits": waits, "/late": late, "/panics": panics} {
		if err := app.RegisterPage(collage.NewPage(strings.TrimPrefix(path, "/")).WithContent(content).WithPath("en", path).Dynamic().Build()); err != nil {
			t.Fatal(err)
		}
	}
	h := app.Handler()
	nextjsGet(h, "/panics", nil) // a first request builds whatever the handler starts once
	before := runtime.NumGoroutine()

	crowd(h, 100, func(int) *http.Request {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_ = cancel // released by its own deadline, as a closed connection's is
		return httptest.NewRequest(http.MethodGet, "/waits", nil).WithContext(ctx)
	})
	crowd(h, 100, func(int) *http.Request { return httptest.NewRequest(http.MethodGet, "/late", nil) })
	for i, rec := range crowd(h, 100, func(int) *http.Request { return httptest.NewRequest(http.MethodGet, "/panics", nil) }) {
		if rec.Code != http.StatusOK && rec.Code < 500 {
			t.Fatalf("panicking request %d: %d", i, rec.Code)
		}
	}
	settled(t, before)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// Shutdown waits for the requests in flight, answers them in full, and stops
// accepting new ones; ListenAndServe then returns as a clean stop.
func TestStress_ShutdownLetsTheRequestsInFlightFinish(t *testing.T) {
	skipShort(t)
	host, portText, _ := net.SplitHostPort(freeAddr(t))
	port, _ := strconv.Atoi(portText)
	entered := make(chan struct{}, 10)
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: host, Port: port},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/keep.html": {Data: []byte(`x`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	slow := collage.NewInlineFragment("slow", `<p>{{.}}</p>`).WithData(collage.Load(
		func(context.Context, *collage.RenderContext) (string, error) {
			entered <- struct{}{}
			time.Sleep(300 * time.Millisecond)
			return "finished", nil
		})).
		Build()
	if err := app.RegisterPage(collage.NewPage("p").WithContent(slow).WithPath("en", "/").Dynamic().Build()); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- app.ListenAndServe() }()

	base := "http://" + net.JoinHostPort(host, portText) + "/"
	var ready bool
	for range 100 {
		if c, err := net.Dial("tcp", net.JoinHostPort(host, portText)); err == nil {
			c.Close()
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("the server never listened")
	}

	type answer struct {
		status int
		body   string
		err    error
	}
	answers := make(chan answer, 5)
	for range 5 {
		go func() {
			resp, err := http.Get(base)
			if err != nil {
				answers <- answer{err: err}
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			answers <- answer{resp.StatusCode, string(body), err}
		}()
	}
	for range 5 {
		<-entered
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Errorf("Shutdown took %v", took)
	}
	for range 5 {
		a := <-answers
		if a.err != nil || a.status != http.StatusOK || !strings.Contains(a.body, "finished") {
			t.Errorf("a request in flight at shutdown: %d %q %v", a.status, a.body, a.err)
		}
	}
	if _, err := net.DialTimeout("tcp", net.JoinHostPort(host, portText), 200*time.Millisecond); err == nil {
		t.Error("the server still accepts connections after Shutdown")
	}
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("ListenAndServe after Shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ListenAndServe never returned")
	}
}

package httpx

import (
	"bufio"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

func TestWithReloadScript(t *testing.T) {
	if got := string(withReloadScript([]byte("<html><body><p>x</p></BODY></html>"))); !strings.Contains(got, devReloadScript+"</BODY>") {
		t.Errorf("script not placed before the closing body tag: %q", got)
	}
	if got := string(withReloadScript([]byte("<p>x</p>"))); got != "<p>x</p>"+devReloadScript {
		t.Errorf("script not appended to a page with no body tag: %q", got)
	}
}

// A development page carries the script; a production one, and the answer to a
// POST, never do.
func TestDevReload_OnlyOnDevelopmentGETs(t *testing.T) {
	page := testPage("home", "/", types.StrategyDynamic)

	if body := newEnv(t, []*types.Page{page}, withDevMode()).get("/").Body.String(); !strings.Contains(body, devReloadPath) {
		t.Errorf("development page has no reload script: %q", body)
	}
	if body := newEnv(t, []*types.Page{page}).get("/").Body.String(); strings.Contains(body, devReloadPath) {
		t.Errorf("production page carries the reload script: %q", body)
	}

	env := newEnv(t, []*types.Page{page}, withDevMode())
	if rec := env.do(httptest.NewRequest(http.MethodPost, "/", nil)); strings.Contains(rec.Body.String(), devReloadPath) {
		t.Errorf("the answer to a POST carries the reload script, and reloading it resubmits: %q", rec.Body.String())
	}
	if rec := env.get("/missing"); !strings.Contains(rec.Body.String(), devReloadPath) {
		t.Errorf("a development error page has no reload script: %q", rec.Body.String())
	}
	if rec := newEnv(t, nil).get(devReloadPath); rec.Code != http.StatusNotFound {
		t.Errorf("GET %s in production = %d, want 404: the stream exists only in development", devReloadPath, rec.Code)
	}
}

// The stream names the process, says reload when a watched file changes, and ends
// when the handler closes its streams.
func TestDevReload_StreamsAChangeAndEndsOnClose(t *testing.T) {
	previous := devReloadInterval
	devReloadInterval = 20 * time.Millisecond
	t.Cleanup(func() { devReloadInterval = previous })

	dir := t.TempDir()
	write := func(content string) {
		path := filepath.Join(dir, "pages", "home.html")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("one")

	env := newEnv(t, nil, withDevMode(), func(d *Deps) { d.DevSources = []fs.FS{os.DirFS(dir)} })
	server := httptest.NewServer(env.handler)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+devReloadPath, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	lines := make(chan string, 32)
	go func() {
		scanner := bufio.NewScanner(res.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	await := func(want string) {
		t.Helper()
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("the stream ended before %q", want)
				}
				if line == want {
					return
				}
			case <-ctx.Done():
				t.Fatalf("no %q on the stream", want)
			}
		}
	}

	await("event: hello")
	// A different size, so the change shows even on a file system that keeps
	// modification times too coarsely to tell the two writes apart.
	write("two, longer")
	await("event: reload")

	env.handler.CloseDevStreams()
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return
			}
		case <-ctx.Done():
			t.Fatal("the stream did not end when the handler closed it")
		}
	}
}

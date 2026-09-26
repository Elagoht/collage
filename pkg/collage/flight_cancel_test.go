package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// A render several requests share belongs to none of them. The reader who started
// it leaving — a closed tab, a stopped reload — must not hand everyone waiting on
// it a page whose parts failed with "context canceled".
func TestSharedRender_OutlivesTheRequestThatStartedIt(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var cancelled atomic.Bool
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/page.html":  {Data: []byte(`<main>{{slot "panel"}}</main>`)},
			"t/panel.html": {Data: []byte(`<p>{{.}}</p>`)},
		}, Root: "t"},
		Cache: collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	panel := collage.NewFragment("panel", "panel.html").Static().WithDataHandler(func(ctx context.Context, _ *collage.RenderContext) (any, []string, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return "measured", nil, nil
		case <-ctx.Done():
			cancelled.Store(true)
			return nil, nil, ctx.Err()
		}
	}).Build()
	page := collage.NewFragment("page", "page.html").WithSlotFragment("panel", panel).Build()
	if err := app.RegisterPage(collage.NewPage("home").WithContent(page).WithPath("en", "/").Build()); err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil).WithContext(leaderCtx))
	<-started

	waiter := make(chan string, 1)
	go func() {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		waiter <- rec.Body.String()
	}()
	time.Sleep(50 * time.Millisecond) // let the waiter join the render
	cancelLeader()
	time.Sleep(50 * time.Millisecond)
	close(release)

	select {
	case body := <-waiter:
		if !strings.Contains(body, "<p>measured</p>") {
			t.Errorf("waiter got %q", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter never answered")
	}
	if cancelled.Load() {
		t.Error("the shared render was cancelled with the request that started it")
	}
}

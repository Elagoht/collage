package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// earlyRecord is what recordingHook saw of one request: its escaped path, the
// client ClientIP named inside OnRequest, the status finish heard, and the
// route RouteOf names, read in finish from the context OnRequest was handed — as
// elagoht/otel names its span.
type earlyRecord struct {
	path   string
	client string
	status int
	route  string
}

// recordingHook records every request OnRequest sees and the status its finish
// is called with — once per call, so a double finish shows as a second record.
type recordingHook struct {
	mu      sync.Mutex
	records []earlyRecord
}

func (p *recordingHook) Name() string                             { return "test/record" }
func (p *recordingHook) Version() string                          { return "0" }
func (p *recordingHook) Init(context.Context, collage.Host) error { return nil }
func (p *recordingHook) Shutdown(context.Context) error           { return nil }
func (p *recordingHook) OnRequest(r *http.Request) (context.Context, func(int)) {
	ctx := r.Context()
	path, client := r.URL.EscapedPath(), collage.ClientIP(r).String()
	return ctx, func(status int) {
		kind, name := collage.RouteOf(ctx)
		p.mu.Lock()
		p.records = append(p.records, earlyRecord{path: path, client: client, status: status, route: kind + ":" + name})
		p.mu.Unlock()
	}
}

func (p *recordingHook) take() []earlyRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.records
	p.records = nil
	return out
}

func earlyApp(t *testing.T, dev bool) (http.Handler, *recordingHook) {
	t.Helper()
	hook := &recordingHook{}
	app, err := collage.New(&collage.Config{
		DevMode: dev,
		Server:  collage.ServerConfig{Host: "localhost", Port: 3000, TrustedProxies: []string{"10.0.0.0/8"}},
		Template: collage.TemplateConfig{
			FS:   fstest.MapFS{"t/p.html": {Data: []byte(`<p>b</p>`)}},
			Root: "t",
		},
		Plugins: []collage.Plugin{hook},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	page := collage.NewPage("b").WithContent(collage.NewFragment("p", "p.html").Build()).WithPath("en", "/b").Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	return app.Handler(), hook
}

// A request collage answers before routing — an encoded slash, a dirty path —
// still runs through every RequestHook, once, with the status it was answered.
func TestRequestHook_SeesEarlyRejections(t *testing.T) {
	h, hook := earlyApp(t, false)
	tests := []struct {
		method, target string
		want           int
		route          string
	}{
		// Decoded, "/x/y" is clean: routing answers it, as before.
		{http.MethodGet, "/x%2fy", http.StatusNotFound, ":"},
		// Decoded, these are dirty: answered before routing, so no route.
		{http.MethodGet, "/x/%2fy", http.StatusNotFound, ":"},
		{http.MethodGet, "/a%2f..%2fb", http.StatusNotFound, ":"},
		{http.MethodGet, "/a/../b", http.StatusMovedPermanently, ":"},
		{http.MethodPost, "/a/../b", http.StatusPermanentRedirect, ":"},
		{http.MethodGet, "/b", http.StatusOK, "page:b"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.target, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.target, nil)
			r.RemoteAddr = "10.0.0.1:1"
			r.Header.Set("X-Forwarded-For", "9.9.9.9")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			got := hook.take()
			if len(got) != 1 {
				t.Fatalf("records = %+v, want exactly one", got)
			}
			if got[0].status != tt.want {
				t.Errorf("finish heard %d, want %d", got[0].status, tt.want)
			}
			if got[0].client != "9.9.9.9" {
				t.Errorf("ClientIP inside OnRequest = %q, want the forwarded 9.9.9.9", got[0].client)
			}
			if got[0].route != tt.route {
				t.Errorf("RouteOf in finish = %q, want %q", got[0].route, tt.route)
			}
			if strings.Contains(tt.target, "%2f") && !strings.Contains(got[0].path, "%2f") {
				t.Errorf("hook saw path %q, want the raw %%2f", got[0].path)
			}
		})
	}
}

// The development reload stream is the tool's own channel, not traffic: no
// RequestHook sees it.
func TestRequestHook_SkipsDevReload(t *testing.T) {
	h, hook := earlyApp(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, path := range []string{"/_collage/reload", "/_collage/reload-worker.js"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
	}
	if got := hook.take(); len(got) != 0 {
		t.Errorf("records = %+v, want none for the dev reload paths", got)
	}
}

package collage_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// mwStamp is middleware that counts the requests it saw and marks each response,
// so a test can tell from either side whether a request passed through it.
type mwStamp struct {
	mu    sync.Mutex
	paths []string
}

func (s *mwStamp) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.mu.Unlock()
		w.Header().Set("X-Stamped", "yes")
		next.ServeHTTP(w, r)
	})
}

func (s *mwStamp) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.paths)
}

// echoSeen answers with the middleware names the request's context carries, the
// ones passage.through added on its way in.
func echoSeen(w http.ResponseWriter, r *http.Request) {
	seen, _ := r.Context().Value(seenKey{}).([]string)
	_, _ = io.WriteString(w, "seen="+strings.Join(seen, ","))
}

func getPath(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// A handler mounted with Handle is the application's own: neither its app.Use
// middleware nor any plugin's runs for it, while a page still gets both.
func TestHandle_BareRunsNoMiddleware(t *testing.T) {
	p := &passage{saw: map[string][]string{}}
	app := orderedApp(t, &wrapper{name: "test/session", names: []string{"session"}, p: p})
	s := &mwStamp{}
	if err := app.Use(s.middleware); err != nil {
		t.Fatal(err)
	}
	if err := app.Use(p.through("app")); err != nil {
		t.Fatal(err)
	}
	if err := app.Handle("/api/", http.HandlerFunc(echoSeen)); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()

	rec := getPath(h, "/api/x")
	if rec.Code != http.StatusOK || rec.Body.String() != "seen=" {
		t.Errorf("GET /api/x = %d %q, want 200 from the handler with no middleware in its context", rec.Code, rec.Body)
	}
	if rec.Header().Get("X-Stamped") != "" {
		t.Error("GET /api/x: the application's middleware stamped a bare mount's response")
	}
	if len(p.path) != 0 || len(s.seen()) != 0 {
		t.Errorf("GET /api/x: middleware ran — plugin and app %v, stamp %v", p.path, s.seen())
	}

	rec = getPath(h, "/")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Stamped") != "yes" {
		t.Errorf("GET / = %d, stamped %q: a page must still pass through middleware", rec.Code, rec.Header().Get("X-Stamped"))
	}
	if want := []string{"session", "app"}; !slices.Equal(p.path, want) {
		t.Errorf("GET /: middleware path = %v, want %v", p.path, want)
	}
}

// WithMiddleware puts one mount back inside the chain, in the order a page gets.
func TestHandle_WithMiddlewareRunsTheChain(t *testing.T) {
	p := &passage{saw: map[string][]string{}}
	app := orderedApp(t, &wrapper{name: "test/session", names: []string{"session"}, p: p})
	s := &mwStamp{}
	if err := app.Use(s.middleware); err != nil {
		t.Fatal(err)
	}
	if err := app.Use(p.through("app")); err != nil {
		t.Fatal(err)
	}
	if err := app.Handle("/api/", http.HandlerFunc(echoSeen), collage.WithMiddleware()); err != nil {
		t.Fatal(err)
	}

	rec := getPath(app.Handler(), "/api/x")
	if rec.Body.String() != "seen=session,app" || rec.Header().Get("X-Stamped") != "yes" {
		t.Errorf("GET /api/x = %q, stamped %q; want the handler inside session then app", rec.Body, rec.Header().Get("X-Stamped"))
	}
	if want := []string{"session", "app"}; !slices.Equal(p.path, want) {
		t.Errorf("middleware path = %v, want %v", p.path, want)
	}
}

// liveish is a plugin that mounts an endpoint of its own with Host.Handle and
// adds a middleware with Host.Use, as collage-live or collage-prometheus does.
type liveish struct{ p *passage }

func (l *liveish) Name() string                   { return "test/live" }
func (l *liveish) Version() string                { return "0" }
func (l *liveish) Shutdown(context.Context) error { return nil }
func (l *liveish) Init(_ context.Context, host collage.Host) error {
	if err := host.Use(l.p.through("plugin")); err != nil {
		return err
	}
	return host.Handle("/_live/", http.HandlerFunc(echoSeen))
}

// A plugin's endpoint is part of the site, so Host.Handle keeps running inside
// the middleware: a session it reads, a basicauth guarding /metrics.
func TestHostHandle_RunsInsideMiddleware(t *testing.T) {
	p := &passage{saw: map[string][]string{}}
	app := orderedApp(t, &liveish{p: p})
	if err := app.Use(p.through("app")); err != nil {
		t.Fatal(err)
	}

	rec := getPath(app.Handler(), "/_live/stream")
	if rec.Code != http.StatusOK || rec.Body.String() != "seen=plugin,app" {
		t.Errorf("GET /_live/stream = %d %q, want the plugin's handler inside plugin then app", rec.Code, rec.Body)
	}
}

// What a bare mount keeps: a plugin's RequestHook ahead of the span, the span,
// the route-naming metric, and the panic guard.
func TestHandle_BareKeepsHooksSpanMetricsAndPanicGuard(t *testing.T) {
	hook, tracer, metrics := &tracingPlugin{}, &parentTracer{}, &routeMetrics{}
	app, err := collage.New(&collage.Config{
		Template:      collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
		Plugins:       []collage.Plugin{hook},
		Observability: collage.ObservabilityConfig{Metrics: metrics, Tracer: tracer},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &mwStamp{}
	if err := app.Use(s.middleware); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := app.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind = collage.RouteInfo(r.Context()).Kind
		trace, _ := r.Context().Value(traceKey{}).(string)
		_, _ = io.WriteString(w, trace)
	})); err != nil {
		t.Fatal(err)
	}
	if err := app.Handle("/boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()

	if rec := getPath(h, "/api/x"); rec.Body.String() != "from-caller" {
		t.Errorf("GET /api/x = %q, want the RequestHook's context to reach the handler", rec.Body)
	}
	if kind != collage.RouteKindHandler {
		t.Errorf("RouteInfo(ctx).Kind inside the handler = %q, want %q", kind, collage.RouteKindHandler)
	}
	if rec := getPath(h, "/boom"); rec.Code != http.StatusInternalServerError {
		t.Errorf("GET /boom = %d, want the panic guard's 500", rec.Code)
	}
	if len(s.seen()) != 0 {
		t.Errorf("middleware ran for a bare mount: %v", s.seen())
	}
	if want := []string{"from-caller", "from-caller"}; !slices.Equal(tracer.parents, want) {
		t.Errorf("span parents = %v, want %v", tracer.parents, want)
	}
	if want := []int{200, 500}; !slices.Equal(hook.finished, want) {
		t.Errorf("RequestHook finish statuses = %v, want %v", hook.finished, want)
	}
	if want := []string{"handler|/api/|/api/|", "handler|/boom|/boom|"}; !slices.Equal(metrics.infos, want) {
		t.Errorf("metric route infos = %v, want %v", metrics.infos, want)
	}
}

// Which requests skip the middleware is decided on the path as it is after the
// pre-routing checks, by the matching the router dispatches with: an exact mount
// claims its one path, a dirty path is redirected or refused before anything —
// middleware or mount — sees it.
func TestHandle_BarePathEdges(t *testing.T) {
	app := orderedApp(t)
	s := &mwStamp{}
	if err := app.Use(s.middleware); err != nil {
		t.Fatal(err)
	}
	var hits []string
	reached := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		w.WriteHeader(http.StatusAccepted)
	})
	if err := app.Handle("/api", reached); err != nil {
		t.Fatal(err)
	}
	if err := app.Handle("/v1/", reached); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()

	for _, c := range []struct {
		target     string
		status     int
		middleware bool // the request passed through app.Use
		handler    bool // the request reached a mount
		location   string
	}{
		{target: "/api", status: http.StatusAccepted, handler: true},
		{target: "/apix", status: http.StatusNotFound, middleware: true},
		{target: "/api/", status: http.StatusNotFound, middleware: true},
		// An encoded slash after an exact mount's path decodes to a longer
		// path, not to the mount's: the exact match is not fooled by it.
		{target: "/api%2Fx", status: http.StatusNotFound, middleware: true},
		{target: "/api%2F", status: http.StatusNotFound, middleware: true},
		{target: "/v1/x", status: http.StatusAccepted, handler: true},
		{target: "/v1", status: http.StatusNotFound, middleware: true},
		// An encoded slash in an otherwise clean path decodes to the path the
		// router dispatches on, so it reaches the mount as it always did — and,
		// being decided by the same match, cannot reach a page with the
		// middleware skipped.
		{target: "/v1%2Fx", status: http.StatusAccepted, handler: true},
		// An encoded slash in a dirty path is refused before anything runs.
		{target: "/x%2F..%2Fv1/y", status: http.StatusNotFound},
		// Dot segments and doubled slashes are redirected to the clean path
		// first, with neither the middleware nor the mount reached.
		{target: "/a/../v1/x", status: http.StatusMovedPermanently, location: "/v1/x"},
		{target: "//v1/x", status: http.StatusMovedPermanently, location: "/v1/x"},
		{target: "/v1/%2e%2e/admin", status: http.StatusMovedPermanently, location: "/admin"},
	} {
		before, hitsBefore := len(s.seen()), len(hits)
		rec := getPath(h, c.target)
		if rec.Code != c.status {
			t.Errorf("GET %s = %d, want %d", c.target, rec.Code, c.status)
		}
		if got := len(s.seen()) > before; got != c.middleware {
			t.Errorf("GET %s: middleware ran = %v, want %v", c.target, got, c.middleware)
		}
		if got := len(hits) > hitsBefore; got != c.handler {
			t.Errorf("GET %s: mount reached = %v, want %v", c.target, got, c.handler)
		}
		if got := rec.Header().Get("Location"); got != c.location {
			t.Errorf("GET %s: Location = %q, want %q", c.target, got, c.location)
		}
	}
}

// A middleware that rewrites a request's path into a bare mount does not reach
// it: the mount is outside the chain, so a request coming out of the chain is
// routed as everything else the chain hands on, and nothing else answers there.
func TestHandle_MiddlewareCannotRewriteIntoABareMount(t *testing.T) {
	app := orderedApp(t)
	if err := app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rest, ok := strings.CutPrefix(r.URL.Path, "/old/"); ok {
				r = r.Clone(r.Context())
				r.URL.Path = "/api/" + rest
			}
			next.ServeHTTP(w, r)
		})
	}); err != nil {
		t.Fatal(err)
	}
	reached := false
	if err := app.Handle("/api/", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()

	if rec := getPath(h, "/old/x"); rec.Code != http.StatusNotFound || reached {
		t.Errorf("GET /old/x rewritten to /api/x = %d, reached %v; want 404 without the bare mount", rec.Code, reached)
	}

	withChain := orderedApp(t)
	_ = withChain.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.URL.Path = strings.Replace(r.URL.Path, "/old/", "/api/", 1)
			next.ServeHTTP(w, r)
		})
	})
	reached = false
	_ = withChain.Handle("/api/", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }), collage.WithMiddleware())
	getPath(withChain.Handler(), "/old/x")
	if !reached {
		t.Error("a WithMiddleware mount: a request rewritten into it did not reach it")
	}
}

package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

type traceKey struct{}

// tracingPlugin puts a trace into the request's context before collage starts.
type tracingPlugin struct {
	mu       sync.Mutex
	finished []int
}

func (p *tracingPlugin) Name() string                             { return "test/trace" }
func (p *tracingPlugin) Version() string                          { return "0" }
func (p *tracingPlugin) Init(context.Context, collage.Host) error { return nil }
func (p *tracingPlugin) Shutdown(context.Context) error           { return nil }
func (p *tracingPlugin) OnRequest(r *http.Request) (context.Context, func(int)) {
	return context.WithValue(r.Context(), traceKey{}, "from-caller"), func(status int) {
		p.mu.Lock()
		p.finished = append(p.finished, status)
		p.mu.Unlock()
	}
}

// parentTracer records what the context collage starts its request span under
// carries.
type parentTracer struct {
	mu      sync.Mutex
	parents []string
}

func (t *parentTracer) StartSpan(ctx context.Context, name string) (context.Context, collage.Span) {
	if name == "collage.http" {
		v, _ := ctx.Value(traceKey{}).(string)
		t.mu.Lock()
		t.parents = append(t.parents, v)
		t.mu.Unlock()
	}
	return ctx, noSpan{}
}

type noSpan struct{}

func (noSpan) SetAttribute(string, string) {}
func (noSpan) RecordError(error)           {}
func (noSpan) End()                        {}

// routeMetrics records the route each response resolved to.
type routeMetrics struct {
	mu     sync.Mutex
	routes []string
	infos  []string
}

func (m *routeMetrics) RenderDuration(context.Context, string, time.Duration, bool)            {}
func (m *routeMetrics) FragmentDuration(context.Context, string, string, time.Duration, error) {}
func (m *routeMetrics) CacheEvent(context.Context, collage.CacheEvent, string)                 {}
func (m *routeMetrics) Invalidation(context.Context, []string, int)                            {}
func (m *routeMetrics) HTTPResponse(ctx context.Context, status int, path string, _ time.Duration) {
	kind, name := collage.RouteOf(ctx)
	info := collage.RouteInfo(ctx)
	m.mu.Lock()
	m.routes = append(m.routes, kind+":"+name)
	m.infos = append(m.infos, info.Kind+"|"+info.Name+"|"+info.Pattern+"|"+info.Locale)
	m.mu.Unlock()
}

// A request hook runs before collage's own span, so a caller's trace is its
// parent; it hears the status; and a metric names the route, not the path.
func TestRequestHookAndRouteOf(t *testing.T) {
	hook, tracer, metrics := &tracingPlugin{}, &parentTracer{}, &routeMetrics{}
	app, err := collage.New(&collage.Config{
		Server:        collage.ServerConfig{Host: "localhost", Port: 3000},
		Template:      collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
		Plugins:       []collage.Plugin{hook},
		Observability: collage.ObservabilityConfig{Metrics: metrics, Tracer: tracer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterPage(collage.NewPage("post").WithContent(collage.NewFragment("p", "p.html").Build()).WithPath("en", "/posts/{slug}").Build()); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterDocument(collage.NewDocument("feed", "application/xml").AtRoot("/feed.xml").WithBody([]byte("<x/>")).Build()); err != nil {
		t.Fatal(err)
	}
	h := app.Handler()
	for _, path := range []string{"/posts/a", "/posts/b", "/feed.xml", "/nowhere"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	if len(tracer.parents) != 4 || tracer.parents[0] != "from-caller" {
		t.Errorf("collage's span started under %q", tracer.parents)
	}
	want := []int{200, 200, 200, 404}
	for i, s := range want {
		if i >= len(hook.finished) || hook.finished[i] != s {
			t.Fatalf("finish statuses = %v, want %v", hook.finished, want)
		}
	}
	wantInfos := []string{"page|post|/posts/{slug}|en", "page|post|/posts/{slug}|en", "document|feed|/feed.xml|en", "|||"}
	for i, w := range wantInfos {
		if i >= len(metrics.infos) || metrics.infos[i] != w {
			t.Fatalf("route infos = %v, want %v", metrics.infos, wantInfos)
		}
	}
	got := metrics.routes
	wantRoutes := []string{"page:post", "page:post", "document:feed", ":"}
	for i, r := range wantRoutes {
		if i >= len(got) || got[i] != r {
			t.Fatalf("routes = %v, want %v", got, wantRoutes)
		}
	}
}

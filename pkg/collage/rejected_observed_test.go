package collage_test

// A request refused by path normalisation — a dot segment or a doubled slash
// redirected to its clean spelling, an encoded slash answered 404 — is reported
// to Metrics and Tracer like any other, under a route label with a bounded set
// of values, never under its raw or decoded path. The request span records the
// method bucketed: a method a client invents is "OTHER".

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

type observedResponse struct {
	status    int
	path      string
	kind      string
	routeName string
}

type observingMetrics struct {
	mu        sync.Mutex
	responses []observedResponse
}

func (m *observingMetrics) RenderDuration(context.Context, string, time.Duration, bool) {}
func (m *observingMetrics) FragmentDuration(context.Context, string, string, time.Duration, error) {
}
func (m *observingMetrics) CacheEvent(context.Context, collage.CacheEvent, string) {}
func (m *observingMetrics) Invalidation(context.Context, []string, int)            {}
func (m *observingMetrics) HTTPResponse(ctx context.Context, status int, path string, _ time.Duration) {
	route := collage.RouteInfo(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, observedResponse{status: status, path: path, kind: route.Kind, routeName: route.Name})
}

type observingSpan struct {
	mu    *sync.Mutex
	attrs map[string]string
}

func (s observingSpan) SetAttribute(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attrs[key] = value
}
func (observingSpan) RecordError(error) {}
func (observingSpan) End()              {}

type observingTracer struct {
	mu    sync.Mutex
	spans []map[string]string
}

func (tr *observingTracer) StartSpan(ctx context.Context, name string) (context.Context, collage.Span) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	attrs := map[string]string{"name": name}
	if name == "collage.http" {
		tr.spans = append(tr.spans, attrs)
	}
	return ctx, observingSpan{mu: &tr.mu, attrs: attrs}
}

func observedSite(t *testing.T) (*collagetest.Site, *observingMetrics, *observingTracer) {
	t.Helper()
	m, tr := &observingMetrics{}, &observingTracer{}
	site := collagetest.NewSite(t, collagetest.WithConfig(func(cfg *collage.Config) {
		cfg.Observability = collage.ObservabilityConfig{Metrics: m, Tracer: tr}
	}))
	return site, m, tr
}

func TestObservability_RejectedPathsAreReportedUnderABoundedLabel(t *testing.T) {
	for _, c := range []struct {
		target string
		status int
	}{
		{"/a/../etc/passwd", http.StatusMovedPermanently},
		{"//p", http.StatusMovedPermanently},
		{"/a%2fb", http.StatusNotFound},
		{"/x%2f..%2f..%2fetc%2fpasswd/..", http.StatusNotFound},
		{"/p%00", http.StatusNotFound},
	} {
		t.Run(c.target, func(t *testing.T) {
			site, m, tr := observedSite(t)
			res := site.Do(httptest.NewRequest(http.MethodGet, c.target, nil))
			if res.Status != c.status {
				t.Fatalf("status = %d, want %d", res.Status, c.status)
			}
			if len(m.responses) != 1 {
				t.Fatalf("Metrics.HTTPResponse calls = %v, want 1", m.responses)
			}
			got := m.responses[0]
			want := observedResponse{status: c.status, path: "", kind: collage.RouteKindRejected, routeName: "normalise"}
			if got != want {
				t.Errorf("metric = %+v, want %+v", got, want)
			}
			if len(tr.spans) != 1 {
				t.Fatalf("collage.http spans = %d, want 1", len(tr.spans))
			}
			span := tr.spans[0]
			if span["http.status_code"] != fmt.Sprint(c.status) {
				t.Errorf("span status = %q", span["http.status_code"])
			}
			if path := span["http.path"]; path != "" {
				t.Errorf("span http.path = %q, want none for a refused path", path)
			}
		})
	}
}

// A 404 for an ordinary unknown path is still a page-kind response under its
// own path: only refused spellings change.
func TestObservability_OrdinaryNotFoundKeepsItsPath(t *testing.T) {
	site, m, tr := observedSite(t)
	site.Get("/nope").WantStatus(http.StatusNotFound)
	if len(m.responses) != 1 || m.responses[0].path != "/nope" || m.responses[0].kind == collage.RouteKindRejected {
		t.Errorf("metric = %+v", m.responses)
	}
	if len(tr.spans) != 1 || tr.spans[0]["http.path"] != "/nope" {
		t.Errorf("spans = %v", tr.spans)
	}
}

// The encoded-slash 404 is still the site's HTML not-found page.
func TestObservability_RejectedNotFoundIsStillHTML(t *testing.T) {
	site, _, _ := observedSite(t)
	for _, target := range []string{"/a%2fb", "/x%2f..%2fy/.."} {
		res := site.Do(httptest.NewRequest(http.MethodGet, target, nil))
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type = %q, want HTML", target, ct)
		}
	}
}

func TestObservability_SpanMethodIsBucketed(t *testing.T) {
	site, _, tr := observedSite(t)
	methods := []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "TRACE", "X00001", "get", "PROPFIND"}
	for _, method := range methods {
		site.Do(httptest.NewRequest(method, "/", nil))
	}
	want := []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "TRACE", "OTHER", "OTHER", "OTHER"}
	if len(tr.spans) != len(want) {
		t.Fatalf("spans = %d, want %d", len(tr.spans), len(want))
	}
	for i, span := range tr.spans {
		if span["http.method"] != want[i] {
			t.Errorf("%s: http.method = %q, want %q", methods[i], span["http.method"], want[i])
		}
	}
}

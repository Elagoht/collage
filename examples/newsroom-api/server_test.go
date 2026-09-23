package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testMux(t *testing.T, chaos *Chaos) http.Handler {
	t.Helper()
	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return newMux(store, slog.New(slog.NewTextHandler(io.Discard, nil)), chaos)
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestAPI_ArticlesReturnsAPage(t *testing.T) {
	rec := get(t, testMux(t, nil), "/v1/articles?per_page=3")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want JSON", ct)
	}

	var page Page
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Items) != 3 {
		t.Errorf("Items = %d, want 3", len(page.Items))
	}
	if page.Total <= 3 {
		t.Errorf("Total = %d, want the whole corpus, not the page size", page.Total)
	}
}

func TestAPI_ArticleNotFoundIs404(t *testing.T) {
	rec := get(t, testMux(t, nil), "/v1/articles/no-such-thing")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestAPI_UnparseablePageFallsBackRatherThanFailing(t *testing.T) {
	// A page number is a hint from a URL a reader may have typed by hand, not a
	// contract. Answering 400 makes a typo look like a broken site.
	rec := get(t, testMux(t, nil), "/v1/articles?page=banana&per_page=-4")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var page Page
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Page != 1 || page.PerPage != DefaultPerPage {
		t.Errorf("page/perPage = %d/%d, want 1/%d", page.Page, page.PerPage, DefaultPerPage)
	}
}

func TestAPI_ChaosFailsOnSchedule(t *testing.T) {
	mux := testMux(t, &Chaos{FailEvery: 3})

	var statuses []int
	for range 6 {
		statuses = append(statuses, get(t, mux, "/v1/articles").Code)
	}

	want := []int{200, 200, 503, 200, 200, 503}
	for i, code := range want {
		if statuses[i] != code {
			t.Fatalf("request %d = %d, want %d (schedule: %v)", i+1, statuses[i], code, statuses)
		}
	}
}

func TestAPI_HealthIsExemptFromChaos(t *testing.T) {
	// A health endpoint the failure injector can knock over stops reporting whether
	// the process is alive and starts reporting whether it drew a short straw. The
	// counter must not advance for /healthz either, or health checks would shift
	// the schedule the rest of the API sees.
	mux := testMux(t, &Chaos{FailEvery: 2})

	for i := range 5 {
		if rec := get(t, mux, "/healthz"); rec.Code != http.StatusOK {
			t.Fatalf("health check %d = %d, want 200", i+1, rec.Code)
		}
	}

	if rec := get(t, mux, "/v1/articles"); rec.Code != http.StatusOK {
		t.Errorf("first API request = %d, want 200; the health checks advanced the failure schedule", rec.Code)
	}
	if rec := get(t, mux, "/v1/articles"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("second API request = %d, want 503", rec.Code)
	}
}

func TestAPI_ChaosCountsEveryRequestNotOnlyFailures(t *testing.T) {
	// If only failures advanced the counter, "every 3rd request" would drift into
	// "every request after the first two".
	mux := testMux(t, &Chaos{FailEvery: 3})

	failures := 0
	for range 9 {
		if get(t, mux, "/v1/articles").Code == http.StatusServiceUnavailable {
			failures++
		}
	}
	if failures != 3 {
		t.Errorf("failures = %d over 9 requests with FailEvery=3, want 3", failures)
	}
}

func TestAPI_LatencyIsApplied(t *testing.T) {
	mux := testMux(t, &Chaos{Latency: 40 * time.Millisecond})

	start := time.Now()
	get(t, mux, "/v1/articles")
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("request took %v, want at least the injected 40ms", elapsed)
	}
}

func TestAPI_EveryClientMethodHasARoute(t *testing.T) {
	// The client and the server are edited separately, and a path typo in either
	// shows up as a 404 that looks exactly like a missing article. This walks the
	// endpoints the client actually calls.
	mux := testMux(t, nil)

	for _, target := range []string{
		"/healthz",
		"/v1/articles",
		"/v1/articles/the-grid-is-the-hard-part",
		"/v1/categories",
		"/v1/categories/climate",
		"/v1/authors",
		"/v1/authors/noor-haddad",
		"/v1/popular?limit=4",
	} {
		if rec := get(t, mux, target); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", target, rec.Code)
		}
	}
}

func TestAPI_PopularRespectsLimit(t *testing.T) {
	rec := get(t, testMux(t, nil), "/v1/popular?limit=4")

	var articles []Article
	if err := json.Unmarshal(rec.Body.Bytes(), &articles); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(articles) != 4 {
		t.Fatalf("got %d articles, want 4", len(articles))
	}
	for i := 1; i < len(articles); i++ {
		if articles[i-1].Views < articles[i].Views {
			t.Error("popular list is not ranked by views")
		}
	}
}

func TestAPI_InjectedFailuresAreLogged(t *testing.T) {
	// An injected 503 must reach the request log. With the middleware wrapped the
	// other way round, chaos short-circuits before the logger runs and the API's
	// own log shows only the requests that succeeded — the one log you cannot
	// afford to be missing while working out why the site went degraded.
	var logged bytes.Buffer
	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	mux := newMux(store, slog.New(slog.NewTextHandler(&logged, nil)), &Chaos{FailEvery: 1})

	if rec := get(t, mux, "/v1/articles"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(logged.String(), "status=503") {
		t.Errorf("request log does not record the injected failure:\n%s", logged.String())
	}
}

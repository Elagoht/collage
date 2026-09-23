package newsroom

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// countingServer is a hostile double: it records how many requests reached it and
// answers however the test tells it to. Counting is the point — the difference
// between "retries a 500" and "retries everything" is invisible in the returned
// error and obvious in the request count.
func countingServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func testClient(baseURL string) *Client {
	c := NewClient(baseURL)
	c.RetryDelay = time.Millisecond
	return c
}

func TestClient_Article_Decodes(t *testing.T) {
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/articles/the-grid" {
			t.Errorf("path = %q, want /v1/articles/the-grid", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"slug":"the-grid","title":"The Grid","publishedAt":"2026-06-13T08:00:00Z"}`))
	})

	art, err := testClient(srv.URL).Article(context.Background(), "the-grid")
	if err != nil {
		t.Fatalf("Article() error = %v", err)
	}
	if art.Title != "The Grid" {
		t.Errorf("Title = %q, want %q", art.Title, "The Grid")
	}
	if art.Path() != "/2026/06/the-grid" {
		t.Errorf("Path() = %q, want /2026/06/the-grid", art.Path())
	}
}

func TestClient_NotFoundIsNotRetried(t *testing.T) {
	// A 404 is a fact about the request. Retrying it multiplies the latency of
	// every mistyped URL and every crawler probing for /wp-admin.
	srv, hits := countingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	})

	c := testClient(srv.URL)
	c.Attempts = 3

	_, err := c.Article(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Article() error = %v, want ErrNotFound", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server saw %d requests, want 1 (a 404 must not be retried)", got)
	}
}

func TestClient_ServerErrorIsRetriedThenReportedUnavailable(t *testing.T) {
	srv, hits := countingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	c := testClient(srv.URL)
	c.Attempts = 3

	_, err := c.Article(context.Background(), "anything")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Article() error = %v, want ErrUnavailable", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("a 500 reported ErrNotFound; conflating the two makes an outage look like a deleted article")
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("server saw %d requests, want 3 (Attempts)", got)
	}
}

func TestClient_RecoversOnRetry(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			http.Error(w, "cold start", http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"slug":"ok","title":"Recovered","publishedAt":"2026-01-01T00:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)

	art, err := testClient(srv.URL).Article(context.Background(), "ok")
	if err != nil {
		t.Fatalf("Article() error = %v, want the second attempt to succeed", err)
	}
	if art.Title != "Recovered" {
		t.Errorf("Title = %q, want %q", art.Title, "Recovered")
	}
}

func TestClient_UnreachableBackendIsUnavailable(t *testing.T) {
	// Closed immediately, so the address refuses connections: the network-failure
	// path, distinct from any HTTP status.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	_, err := testClient(addr).Article(context.Background(), "anything")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Article() error = %v, want ErrUnavailable", err)
	}
}

func TestClient_CancelledContextStopsRetrying(t *testing.T) {
	// The caller has given up — typically the fragment timeout firing. Continuing
	// to sleep and retry spends the backend's capacity on a response nobody reads.
	srv, hits := countingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	c := testClient(srv.URL)
	c.Attempts = 5
	c.RetryDelay = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := c.Article(ctx, "anything")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Article() error = %v, want ErrUnavailable", err)
	}
	if got := hits.Load(); got > 2 {
		t.Errorf("server saw %d requests after the context expired, want at most 2", got)
	}
}

func TestClient_MalformedJSONIsUnavailableNotNotFound(t *testing.T) {
	srv, _ := countingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"slug": `))
	})

	c := testClient(srv.URL)
	c.Attempts = 1

	_, err := c.Article(context.Background(), "anything")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Article() error = %v, want ErrUnavailable", err)
	}
}

func TestClient_ArticlesSendsEveryFilter(t *testing.T) {
	var got map[string]string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = map[string]string{}
		for k, v := range r.URL.Query() {
			got[k] = v[0]
		}
		w.Write([]byte(`{"items":[],"page":2,"perPage":3,"total":0,"totalPages":0}`))
	})

	_, err := testClient(srv.URL).Articles(context.Background(), Filter{
		Category: "climate", Author: "noor-haddad", Query: "grid", Page: 2, PerPage: 3,
	})
	if err != nil {
		t.Fatalf("Articles() error = %v", err)
	}

	want := map[string]string{"category": "climate", "author": "noor-haddad", "q": "grid", "page": "2", "per_page": "3"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("query %q = %q, want %q", k, got[k], v)
		}
	}
}

func TestClient_HealthDoesNotRetry(t *testing.T) {
	// A readiness probe that retries reports the state of the last few seconds
	// rather than of now.
	srv, hits := countingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})

	c := testClient(srv.URL)
	c.Attempts = 4

	if err := c.Health(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Health() error = %v, want ErrUnavailable", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server saw %d requests, want 1", got)
	}
}

func TestClient_SlugIsPathEscaped(t *testing.T) {
	// A slug arrives from the URL, so it is attacker-controlled. Without escaping,
	// "../authors/x" would walk the client onto a different endpoint entirely.
	var seen string
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.EscapedPath()
		http.Error(w, "nope", http.StatusNotFound)
	})

	_, _ = testClient(srv.URL).Article(context.Background(), "../authors/noor-haddad")

	if seen != "/v1/articles/..%2Fauthors%2Fnoor-haddad" {
		t.Errorf("request path = %q, want the slug escaped into a single segment", seen)
	}
}

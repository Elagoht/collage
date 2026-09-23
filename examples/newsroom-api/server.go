package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// Chaos injects the failure modes a real backend has and a fake one otherwise
// never demonstrates. Without it the site's degraded-rendering paths are code that
// compiles and has never once run.
//
// The failure schedule is deterministic — every Nth request — rather than random.
// A probability would be more lifelike and would make every test that exercises it
// flaky, and a flaky test in an example is worse than a slightly unlifelike knob.
type Chaos struct {
	// Latency is added to every response before it is written.
	Latency time.Duration
	// FailEvery makes every Nth request answer 503. Zero disables it.
	FailEvery int

	seen atomic.Int64
}

// tripped reports whether this request is one of the scheduled failures, and counts
// the request either way. Counting unconditionally is what keeps the schedule
// honest: if only failures advanced the counter, "every 3rd request" would mean
// "every request after the first two".
func (c *Chaos) tripped() bool {
	n := c.seen.Add(1)
	return c.FailEvery > 0 && n%int64(c.FailEvery) == 0
}

// server holds everything the handlers read.
type server struct {
	store *Store
	log   *slog.Logger
	chaos *Chaos
}

// newMux wires the API's routes.
//
// Every listing endpoint answers the same Page shape, so the site's client
// has one decode path for the home page, the category pages, the author pages and
// search. That uniformity is why the site needs no endpoint-specific parsing.
func newMux(store *Store, log *slog.Logger, chaos *Chaos) http.Handler {
	s := &server{store: store, log: log, chaos: chaos}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/articles", s.articles)
	mux.HandleFunc("GET /v1/articles/{slug}", s.article)
	mux.HandleFunc("GET /v1/categories", s.categories)
	mux.HandleFunc("GET /v1/categories/{slug}", s.category)
	mux.HandleFunc("GET /v1/authors", s.authors)
	mux.HandleFunc("GET /v1/authors/{slug}", s.author)
	mux.HandleFunc("GET /v1/popular", s.popular)
	// Not under /v1: an image is not part of the JSON API's versioned surface, and
	// a client fetching one is a browser rather than the site's own code.
	mux.HandleFunc("GET /images/{slug}", s.serveImage)

	// Logging is the outer layer so it records what the client actually received.
	// Wrapped the other way round, an injected 503 short-circuits before the logger
	// ever runs and the API's own request log shows only the requests that
	// succeeded — which is precisely the log you cannot afford to be missing while
	// you are debugging why the site went degraded.
	return s.withLogging(s.withChaos(mux))
}

// health answers without consulting Chaos. A health endpoint that the failure
// injector can knock over stops reporting whether the process is alive and starts
// reporting whether it drew a short straw.
func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}` + "\n"))
}

func (s *server) articles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := s.store.List(Filter{
		Category: q.Get("category"),
		Author:   q.Get("author"),
		Query:    q.Get("q"),
		Page:     atoiOr(q.Get("page"), 1),
		PerPage:  atoiOr(q.Get("per_page"), 0),
	})
	writeJSON(w, http.StatusOK, page)
}

func (s *server) article(w http.ResponseWriter, r *http.Request) {
	art, err := s.store.Article(r.PathValue("slug"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, art)
}

func (s *server) categories(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Categories())
}

func (s *server) category(w http.ResponseWriter, r *http.Request) {
	cat, err := s.store.Category(r.PathValue("slug"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cat)
}

func (s *server) authors(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Authors())
}

func (s *server) author(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.Author(r.PathValue("slug"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *server) popular(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Popular(atoiOr(r.URL.Query().Get("limit"), 5)))
}

// writeError maps a domain error onto a status code. Only ErrNotFound becomes a
// 404; anything else is a 500, because a backend that answers 404 for conditions it
// does not recognise teaches its callers that unknown failures mean "deleted".
func (s *server) writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	s.log.Error("api: request failed", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

// withChaos applies the injected latency and failure schedule. /healthz is exempt,
// as health explains.
func (s *server) withChaos(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.chaos == nil || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if s.chaos.Latency > 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(s.chaos.Latency):
			}
		}
		if s.chaos.tripped() {
			s.log.Warn("api: injected failure", "path", r.URL.Path)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "injected failure"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withLogging records one line per request with the status and duration.
func (s *server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("api: request",
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"status", rec.status,
			"duration", time.Since(start),
		)
	})
}

// statusRecorder remembers the status code so the log line can report it. It
// records only the first WriteHeader, which is the one that reaches the client.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.written {
		r.status = code
		r.written = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.written = true
	return r.ResponseWriter.Write(b)
}

func writeJSON(w http.ResponseWriter, status int, value any) { // any: the JSON encoder's own parameter type
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		// The header is already written, so there is no status left to change.
		// Nothing useful remains but to stop.
		return
	}
}

// atoiOr parses s, falling back to def for anything that is not a positive integer.
// A listing endpoint should answer "?page=banana" with page one rather than a 400:
// the parameter is a hint from a URL a reader may have typed, not a contract.
func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return def
	}
	return n
}

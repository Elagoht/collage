package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

func TestRunGuardsShortCircuits(t *testing.T) {
	var asked []string
	g := func(name string, d *types.GuardDecision) types.GuardFunc {
		return func(ctx context.Context, r *http.Request) (*types.GuardDecision, error) {
			asked = append(asked, name)
			return d, nil
		}
	}
	d, err := runGuards(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil), []types.GuardFunc{
		g("first", nil),
		g("second", &types.GuardDecision{Status: http.StatusSeeOther, Location: "/login"}),
		g("third", nil),
	})
	if err != nil || d == nil || d.Location != "/login" {
		t.Fatalf("runGuards = %v, %v; want the second's decision", d, err)
	}
	if len(asked) != 2 {
		t.Fatalf("asked = %v, want first and second only", asked)
	}
}

func TestRunGuardsStopsOnError(t *testing.T) {
	var asked int
	failing := func(ctx context.Context, r *http.Request) (*types.GuardDecision, error) {
		asked++
		return nil, errors.New("session store down")
	}
	after := func(ctx context.Context, r *http.Request) (*types.GuardDecision, error) {
		t.Fatal("a guard after a failing guard ran")
		return nil, nil
	}
	if _, err := runGuards(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil), []types.GuardFunc{failing, after}); err == nil {
		t.Fatal("runGuards swallowed the error")
	}
	if asked != 1 {
		t.Fatalf("asked %d guards, want 1", asked)
	}
}

func TestWriteRedirectFetchConvention(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set(FetchHeader, "1")
	status := writeRedirect(w, r, http.StatusSeeOther, "/login")
	if status != http.StatusNoContent || w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 for a fetch-marked request", status)
	}
	if got := w.Header().Get(LocationHeader); got != "/login" {
		t.Fatalf("Collage-Location = %q, want /login", got)
	}
}

// A guard's answer is about one reader. Kept by a CDN that caches a 404 or a 308
// by default, a logged-out reader's refusal would be served to everyone after.
func TestCheckGuards_AnswerIsNotStored(t *testing.T) {
	h := &Handler{}
	for _, d := range []*types.GuardDecision{
		{Status: http.StatusNotFound},
		{Status: http.StatusPermanentRedirect, Location: "/login"},
	} {
		for _, fetch := range []bool{false, true} {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/private", nil)
			if fetch {
				r.Header.Set(FetchHeader, "1")
			}
			guard := func(context.Context, *http.Request) (*types.GuardDecision, error) { return d, nil }
			if _, allowed := h.checkGuards(w, r, &routeRef{}, "en", []types.GuardFunc{guard}); allowed {
				t.Fatal("checkGuards allowed a refused request")
			}
			if got := w.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%d (fetch %v): Cache-Control = %q, want no-store", d.Status, fetch, got)
			}
		}
	}
}

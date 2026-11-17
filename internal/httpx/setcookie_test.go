package httpx

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// A response carrying a cookie a middleware wrote must not be marked public: a
// CDN would store it with its Set-Cookie and give one reader's session cookie to
// every reader after.
func TestHandler_SetCookieFromMiddlewareMakesTheResponsePrivate(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page}, func(d *Deps) {
		d.Middleware = append(d.Middleware, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc"})
				next.ServeHTTP(w, r)
			})
		})
	})

	for i, name := range []string{"miss", "hit"} {
		rec := env.get("/")
		if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("%s (request %d): Cache-Control = %q, want private, no-store", name, i, got)
		}
		if strings.Contains(rec.Header().Get("Vary"), "Accept") {
			t.Errorf("%s: Vary = %q, want none on a private response", name, rec.Header().Get("Vary"))
		}
	}
}

// Without a cookie the response is public as before.
func TestHandler_NoSetCookieStaysPublic(t *testing.T) {
	page := testPage("home", "/", types.StrategyStatic)
	env := newEnv(t, []*types.Page{page})
	if got := env.get("/").Header().Get("Cache-Control"); !strings.HasPrefix(got, "public") {
		t.Fatalf("Cache-Control = %q, want public", got)
	}
}

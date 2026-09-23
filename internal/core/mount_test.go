package core

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/types"
)

// mountFS returns a small file system a Mount can serve, distinct in content from
// every template fixture so a test cannot mistake an asset response for a
// rendered page.
func mountFS() fstest.MapFS {
	return fstest.MapFS{
		"app.css": {Data: []byte("body{color:red}")},
	}
}

// newStaticDocument returns a minimal, valid document fixture registered at path,
// used by the tests below to prove a mount can shadow a document exactly as it can
// a page.
func newStaticDocument(path string) *types.Document {
	return &types.Document{
		Name:        "data",
		ContentType: "application/json",
		Paths:       map[string]string{"en": path},
		Handler: func(context.Context, *types.RenderContext) ([]byte, []string, error) {
			return []byte("{}"), nil, nil
		},
	}
}

// TestMount_ServesAFileAlongsidePages proves a mount and a page coexist on one
// App: the page still renders at its own path and the mount serves its files at
// its own prefix, in the same handler.
func TestMount_ServesAFileAlongsidePages(t *testing.T) {
	app := newTestApp(t, nil)
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.Mount("/static/", mountFS()); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	handler := app.Handler()

	page := get(handler, "/")
	if page.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", page.Code, http.StatusOK)
	}
	if !strings.Contains(page.Body.String(), "Welcome Home") {
		t.Fatalf("GET / body = %q, want the rendered page", page.Body.String())
	}

	asset := get(handler, "/static/app.css")
	if asset.Code != http.StatusOK {
		t.Fatalf("GET /static/app.css status = %d, want %d", asset.Code, http.StatusOK)
	}
	if got := asset.Body.String(); got != "body{color:red}" {
		t.Fatalf("GET /static/app.css body = %q, want the mounted file's content", got)
	}
}

// TestMount_RejectsAPrefixShadowingARegisteredPage proves the close-out check
// catches a mount registered after the page whose route it would swallow, and
// that the error names both the mount prefix and the page it shadows.
func TestMount_RejectsAPrefixShadowingARegisteredPage(t *testing.T) {
	app := newTestApp(t, nil)

	page := newHomePage()
	page.Paths = map[string]string{"en": "/static/thing"}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.Mount("/static/", mountFS()); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	err := app.Start()
	if !errors.Is(err, ErrMountShadowsRoute) {
		t.Fatalf("Start = %v, want ErrMountShadowsRoute", err)
	}
	for _, want := range []string{"/static/", "/static/thing"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// TestMount_RejectsAPrefixShadowingADocument mirrors the page case for a
// document: a mount prefix that swallows a registered document's path must be
// rejected the same way.
func TestMount_RejectsAPrefixShadowingADocument(t *testing.T) {
	app := newTestApp(t, nil)

	doc := newStaticDocument("/static/data.json")
	if err := app.RegisterDocument(doc); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}
	if err := app.Mount("/static/", mountFS()); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	err := app.Start()
	if !errors.Is(err, ErrMountShadowsRoute) {
		t.Fatalf("Start = %v, want ErrMountShadowsRoute", err)
	}
	for _, want := range []string{"/static/", "/static/data.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// TestMount_ShadowCheckIsOrderIndependent registers the mount before the page it
// shadows, proving the check runs at buildHandler's close-out rather than at
// Mount's own call time — the entire reason it lives there rather than in Mount.
func TestMount_ShadowCheckIsOrderIndependent(t *testing.T) {
	app := newTestApp(t, nil)

	if err := app.Mount("/static/", mountFS()); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	page := newHomePage()
	page.Paths = map[string]string{"en": "/static/thing"}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	err := app.Start()
	if !errors.Is(err, ErrMountShadowsRoute) {
		t.Fatalf("Start = %v, want ErrMountShadowsRoute (mount registered first)", err)
	}
}

// TestMount_RejectsTwoOverlappingMounts proves two mounts whose prefixes overlap
// — one is a prefix of the other — are rejected, regardless of which the router
// would have preferred.
func TestMount_RejectsTwoOverlappingMounts(t *testing.T) {
	app := newTestApp(t, nil)

	if err := app.Mount("/static/", mountFS()); err != nil {
		t.Fatalf("Mount(/static/): %v", err)
	}
	if err := app.Mount("/static/img/", mountFS()); err != nil {
		t.Fatalf("Mount(/static/img/): %v", err)
	}

	err := app.Start()
	if !errors.Is(err, ErrMountConflict) {
		t.Fatalf("Start = %v, want ErrMountConflict", err)
	}
	for _, want := range []string{"/static/", "/static/img/"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// TestMount_RejectsAfterStart proves Mount closes exactly like RegisterPage and
// RegisterDocument: once the handler has been built, a further mount is refused.
func TestMount_RejectsAfterStart(t *testing.T) {
	app := newTestApp(t, nil)
	app.Handler()

	if err := app.Mount("/static/", mountFS()); !errors.Is(err, ErrAppStarted) {
		t.Fatalf("Mount after start = %v, want ErrAppStarted", err)
	}
}

// TestMount_DoesNotEnterThePageCache proves a mounted file's freshness is left
// entirely to the client and the mount's own Cache-Control, never the page cache:
// serving a mounted file must leave the cache with zero entries.
func TestMount_DoesNotEnterThePageCache(t *testing.T) {
	store := cache.NewMemory(cache.MemoryConfig{})
	app := newTestApp(t, func(cfg *Config) {
		cfg.Cache.Store = store
	})
	if err := app.Mount("/static/", mountFS()); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	handler := app.Handler()
	asset := get(handler, "/static/app.css")
	if asset.Code != http.StatusOK {
		t.Fatalf("GET /static/app.css status = %d, want %d", asset.Code, http.StatusOK)
	}

	if entries := store.Stats().Entries; entries != 0 {
		t.Fatalf("cache entries = %d, want 0: a mounted file must never enter the page cache", entries)
	}
}

// TestMount_RejectsAPrefixShadowingARedirect is the regression for the close-out
// check reading core's own registries instead of asking the router. A registered
// Redirect.From lives in the router and nowhere else, so a check that walked
// a.pages and a.documents could not see it: Mount("/static/") alongside a page
// carrying Redirect{From: "/static/old.css"} started cleanly, and the redirect's
// URL answered 404 with no Location header. A redirect colliding with a route is a
// startup error in either order everywhere else in this framework, and a mount is
// not an exception to that.
func TestMount_RejectsAPrefixShadowingARedirect(t *testing.T) {
	app := newTestApp(t, nil)

	page := newHomePage()
	page.Redirects = []*types.Redirect{{From: "/static/old.css", To: "/assets/app.css", Permanent: true}}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.Mount("/static/", mountFS()); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	err := app.Start()
	if !errors.Is(err, ErrMountShadowsRoute) {
		t.Fatalf("Start = %v, want ErrMountShadowsRoute — a mount must not silently swallow a registered redirect", err)
	}
	for _, want := range []string{"/static/", "/static/old.css"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// TestMount_RejectsARedirectShadowedByAnEarlierMount is the other ordering. The
// close-out check exists precisely so registration order cannot decide whether a
// collision is reported, and a redirect must be no different from a page path on
// that point.
func TestMount_RejectsARedirectShadowedByAnEarlierMount(t *testing.T) {
	app := newTestApp(t, nil)

	if err := app.Mount("/static/", mountFS()); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	page := newHomePage()
	page.Redirects = []*types.Redirect{{From: "/static/old.css", To: "/assets/app.css", Permanent: true}}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	err := app.Start()
	if !errors.Is(err, ErrMountShadowsRoute) {
		t.Fatalf("Start = %v, want ErrMountShadowsRoute — the mount-first ordering must fail identically", err)
	}
	for _, want := range []string{"/static/", "/static/old.css"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// TestMount_RejectsAPrefixShadowingALocalePrefixedPage covers the locale half of
// the same root cause. A page registered at Paths{"tr": "/about"} is reached at
// "/tr/about", because path-locale resolution strips "/tr" before matching — so a
// mount at "/tr/" swallows it, and no comparison against the registered pattern
// "/about" can show that. The router reports the locale-prefixed form of every
// route for exactly this reason.
func TestMount_RejectsAPrefixShadowingALocalePrefixedPage(t *testing.T) {
	app := newTestApp(t, func(cfg *Config) {
		cfg.Locale.Default = "en"
		cfg.Locale.Supported = []string{"en", "tr"}
	})

	page := newHomePage()
	page.Paths = map[string]string{"en": "/about", "tr": "/about"}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.Mount("/tr/", mountFS()); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	err := app.Start()
	if !errors.Is(err, ErrMountShadowsRoute) {
		t.Fatalf("Start = %v, want ErrMountShadowsRoute — /tr/about is the page's real URL", err)
	}
	if !strings.Contains(err.Error(), "/tr/about") {
		t.Fatalf("error %q does not name the locale-prefixed URL /tr/about", err)
	}
}

// TestMount_RejectsAPrefixShadowingALocaleHomePage is the trailing-slash corner of
// the same check. A page whose Turkish path is "/" is reached at "/tr/" as well as
// "/tr" — path-locale resolution maps a single-segment path to "/" — but
// ClaimedPaths reported only the unslashed form, so strings.HasPrefix("/tr", "/tr/")
// was false and a mount at "/tr/" was allowed to swallow the Turkish front page.
func TestMount_RejectsAPrefixShadowingALocaleHomePage(t *testing.T) {
	app := newTestApp(t, func(cfg *Config) {
		cfg.Locale.Default = "en"
		cfg.Locale.Supported = []string{"en", "tr"}
	})

	page := newHomePage()
	page.Paths = map[string]string{"en": "/", "tr": "/"}
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.Mount("/tr/", mountFS()); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	err := app.Start()
	if !errors.Is(err, ErrMountShadowsRoute) {
		t.Fatalf("Start = %v, want ErrMountShadowsRoute — /tr/ is the Turkish front page", err)
	}
}

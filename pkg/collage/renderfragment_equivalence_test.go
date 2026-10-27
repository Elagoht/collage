package collage_test

// RenderFragment is equivalent to an HTTP request for the fragment's URL: the
// guard sees the URL's path values and nothing of the caller's, the middleware
// chain runs for the fragment's path, and every PersonaliseHook rewrites the
// result. These are the audit of 2026-10-27's findings HOST-07, PLUGA-B01,
// HOOKS-01 (PLUGA-01, PLUGA-B03) and HOOKS-07, asserted fixed.

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// rfePlugin hands its Host to the test and adds the middleware it is given.
type rfePlugin struct {
	name       string
	host       collage.Host
	middleware []func(http.Handler) http.Handler
}

func (p *rfePlugin) Name() string                   { return p.name }
func (p *rfePlugin) Version() string                { return "0" }
func (p *rfePlugin) Shutdown(context.Context) error { return nil }
func (p *rfePlugin) Init(_ context.Context, h collage.Host) error {
	p.host = h
	for _, m := range p.middleware {
		if err := h.Use(m); err != nil {
			return err
		}
	}
	return nil
}

// rfeMarker stands in for collage-secure's key-derived nonce placeholder.
const rfeMarker = "collage-csp-nonce-0123456789abcdef0123456789abcdef"

// rfeNonce mirrors collage-secure: {{cspNonce}} writes the marker, OnPersonalise
// swaps it for a per-response nonce and marks the body personal.
type rfeNonce struct{ rfePlugin }

func (p *rfeNonce) Configure(_ context.Context, h collage.ConfigHost) error {
	return h.AddTemplateFunc("cspNonce", func() string { return rfeMarker })
}

func (p *rfeNonce) OnPersonalise(_ context.Context, ev *collage.PersonaliseEvent) error {
	if !bytes.Contains(ev.Body, []byte(rfeMarker)) {
		return nil
	}
	ev.Body = bytes.ReplaceAll(ev.Body, []byte(rfeMarker), []byte("nonce-xyz"))
	ev.Personal = true
	return nil
}

// rfeLater is a later PersonaliseHook that assigns Personal from a condition of
// its own, false here.
type rfeLater struct{ rfePlugin }

func (p *rfeLater) OnPersonalise(_ context.Context, ev *collage.PersonaliseEvent) error {
	ev.Personal = bytes.Contains(ev.Body, []byte("data-reader"))
	return nil
}

// rfeBoards is a page at /boards/{id} opening its board at /boards/{id}/live,
// guarded by guard, rendering the id it was given.
func rfeBoards(guard collage.GuardFunc) *collage.Page {
	board := collage.NewFragment("board", "board.html").WithGuard(guard).WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (string, error) { return rc.Param("id"), nil })).Build()
	return collage.NewPage("board").WithContent(board).WithPath("en", "/boards/{id}").
		WithFragmentPath("en", "/boards/{id}/live", board).Dynamic().Build()
}

func rfeHTML(r *collage.FragmentRender) string {
	if r == nil {
		return ""
	}
	return string(r.HTML)
}

// HOST-07: a guard deciding on r.PathValue meets the fragment URL's values,
// through RenderFragment as over HTTP, by path and by name.
func TestRenderFragment_GuardSeesPathValues(t *testing.T) {
	var mu sync.Mutex
	var saw []string
	deny := func(_ context.Context, r *http.Request) (*collage.GuardDecision, error) {
		mu.Lock()
		saw = append(saw, r.PathValue("id"))
		mu.Unlock()
		if r.PathValue("id") == "secret" {
			return &collage.GuardDecision{Status: http.StatusForbidden}, nil
		}
		return nil, nil
	}
	p := &rfePlugin{name: "test/live"}
	site := collagetest.NewSite(t, collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"board.html": `board {{.}}`}),
		collagetest.WithRegister(rfeBoards(deny)))
	site.Get("/boards/secret/live").WantStatus(http.StatusForbidden)

	stream := httptest.NewRequest(http.MethodGet, "/_live", nil)
	if got, err := p.host.RenderFragment(stream, collage.FragmentRequest{Path: "/boards/secret/live"}); !errors.Is(err, collage.ErrGuardRefused) {
		t.Errorf("by path: %q, %v; want ErrGuardRefused", rfeHTML(got), err)
	}
	if got, err := p.host.RenderFragment(stream, collage.FragmentRequest{Page: "board", Fragment: "board", Params: map[string]string{"id": "secret"}}); !errors.Is(err, collage.ErrGuardRefused) {
		t.Errorf("by name: %q, %v; want ErrGuardRefused", rfeHTML(got), err)
	}
	got, err := p.host.RenderFragment(stream, collage.FragmentRequest{Page: "board", Fragment: "board", Params: map[string]string{"id": "open"}})
	if err != nil || rfeHTML(got) != "board open" {
		t.Errorf("by name, open board: %q, %v", rfeHTML(got), err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range saw {
		if id == "" {
			t.Errorf("a guard saw no id: %q", saw)
		}
	}
}

// HOST-07b: the caller's own path values never reach the guard. An allow-list
// guard is neither bypassed by a caller carrying the allowed value nor denies a
// legitimate reader whose caller carries none.
func TestRenderFragment_CallerPathValuesDoNotLeak(t *testing.T) {
	var leaked []string
	allow := func(_ context.Context, r *http.Request) (*collage.GuardDecision, error) {
		if v := r.PathValue("other"); v != "" {
			leaked = append(leaked, v)
		}
		if r.PathValue("id") != "public" {
			return &collage.GuardDecision{Status: http.StatusForbidden}, nil
		}
		return nil, nil
	}
	p := &rfePlugin{name: "test/live"}
	collagetest.NewSite(t, collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"board.html": `board {{.}}`}),
		collagetest.WithRegister(rfeBoards(allow)))

	carrying := httptest.NewRequest(http.MethodGet, "/x", nil)
	carrying.SetPathValue("id", "public")
	carrying.SetPathValue("other", "x")
	if got, err := p.host.RenderFragment(carrying, collage.FragmentRequest{Path: "/boards/secret/live"}); !errors.Is(err, collage.ErrGuardRefused) {
		t.Errorf("caller carrying id=public, path secret: %q, %v; want ErrGuardRefused", rfeHTML(got), err)
	}
	bare := httptest.NewRequest(http.MethodGet, "/x", nil)
	if got, err := p.host.RenderFragment(bare, collage.FragmentRequest{Path: "/boards/public/live"}); err != nil || rfeHTML(got) != "board public" {
		t.Errorf("bare caller, path public: %q, %v", rfeHTML(got), err)
	}
	if _, err := p.host.RenderFragment(carrying, collage.FragmentRequest{Path: "/boards/public/live"}); err != nil {
		t.Errorf("caller carrying values, path public: %v", err)
	}
	if len(leaked) > 0 {
		t.Errorf("the guard read the caller's own path value %q", leaked)
	}
}

// HOST-07b: by name, parameters the router could never produce are refused as
// a URL that reaches no fragment.
func TestRenderFragment_ByNameRefusesUnproducibleParams(t *testing.T) {
	open := func(context.Context, *http.Request) (*collage.GuardDecision, error) { return nil, nil }
	p := &rfePlugin{name: "test/live"}
	collagetest.NewSite(t, collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"board.html": `board {{.}}`}),
		collagetest.WithRegister(rfeBoards(open)))
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	for _, params := range []map[string]string{
		{"id": "a/../b"},
		{"id": "a/b"},
		{"id": "."},
		{"id": ".."},
		{"id": ""},
		nil,
		{"id": "x", "undeclared": "y"},
	} {
		got, err := p.host.RenderFragment(r, collage.FragmentRequest{Page: "board", Fragment: "board", Params: params})
		if !errors.Is(err, collage.ErrUnknownFragmentPath) {
			t.Errorf("params %v: %q, %v; want ErrUnknownFragmentPath", params, rfeHTML(got), err)
		}
	}
	got, err := p.host.RenderFragment(r, collage.FragmentRequest{Page: "board", Fragment: "board", Params: map[string]string{"id": "v1.2 b?c"}})
	if err != nil || rfeHTML(got) != "board v1.2 b?c" {
		t.Errorf("a value the router produces: %q, %v", rfeHTML(got), err)
	}
}

// rfeBasicAuth refuses every request under prefix without the right
// Authorization header, as collage-basicauth's Protect does, writing a body of
// its own.
func rfeBasicAuth(prefix string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, prefix) {
				if user, pass, ok := r.BasicAuth(); !ok || user != "admin" || pass != "pw" {
					w.Header().Set("WWW-Authenticate", `Basic realm="admin"`)
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, "denied by middleware")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rfeStats is a page at /admin opening its stats at /admin/stats.
func rfeStats() *collage.Page {
	stats := collage.NewFragment("stats", "stats.html").Build()
	return collage.NewPage("admin").WithContent(stats).WithPath("en", "/admin").
		WithFragmentPath("en", "/admin/stats", stats).Build()
}

// PLUGA-B01: a path-scoped middleware protects a fragment path from
// RenderFragment as it does from HTTP, whether a plugin or the application
// added it, and lets through a reader it admits.
func TestRenderFragment_RunsMiddleware(t *testing.T) {
	for _, byPlugin := range []bool{true, false} {
		p := &rfePlugin{name: "test/live"}
		guard := &rfePlugin{name: "test/basicauth"}
		options := []collagetest.SiteOption{
			collagetest.WithTemplates(map[string]string{"stats.html": `<p>revenue=1234567</p>`}),
			collagetest.WithRegister(rfeStats()),
		}
		if byPlugin {
			guard.middleware = append(guard.middleware, rfeBasicAuth("/admin"))
			options = append(options, collagetest.WithPlugins(p, guard))
		} else {
			options = append(options, collagetest.WithPlugins(p), collagetest.WithSetup(func(app *collage.App) error {
				return app.Use(rfeBasicAuth("/admin"))
			}))
		}
		site := collagetest.NewSite(t, options...)
		site.Get("/admin/stats").WantStatus(http.StatusUnauthorized)

		stream := httptest.NewRequest(http.MethodGet, "/_live/stream/?f=/admin/stats", nil)
		for _, req := range []collage.FragmentRequest{{Path: "/admin/stats"}, {Page: "admin", Fragment: "stats"}} {
			got, err := p.host.RenderFragment(stream, req)
			if !errors.Is(err, collage.ErrGuardRefused) || got != nil {
				t.Fatalf("byPlugin=%v %+v without credentials: %q, %v; want ErrGuardRefused", byPlugin, req, rfeHTML(got), err)
			}
			if !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "denied by middleware") {
				t.Errorf("byPlugin=%v: error %q should name the status and nothing the middleware wrote", byPlugin, err)
			}
		}
		withCreds := httptest.NewRequest(http.MethodGet, "/_live/stream/?f=/admin/stats", nil)
		withCreds.SetBasicAuth("admin", "pw")
		got, err := p.host.RenderFragment(withCreds, collage.FragmentRequest{Path: "/admin/stats"})
		if err != nil || rfeHTML(got) != "<p>revenue=1234567</p>" {
			t.Errorf("byPlugin=%v with credentials: %q, %v", byPlugin, rfeHTML(got), err)
		}
	}
}

type rfeUserKey struct{}

// Middleware runs in the order ServeHTTP runs it, sees the fragment's path and
// a request with no body, and what it puts in the context reaches the guard and
// the render.
func TestRenderFragment_MiddlewareOrderAndContext(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil || len(body) != 0 {
					t.Errorf("%s read body %q, %v", name, body, err)
				}
				if err := r.ParseForm(); err != nil {
					t.Errorf("%s ParseForm: %v", name, err)
				}
				mu.Lock()
				order = append(order, name+" "+r.URL.Path)
				mu.Unlock()
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), rfeUserKey{}, "ada")))
			})
		}
	}
	var guardSaw string
	guard := func(_ context.Context, r *http.Request) (*collage.GuardDecision, error) {
		guardSaw, _ = r.Context().Value(rfeUserKey{}).(string)
		return nil, nil
	}
	who := collage.NewFragment("who", "who.html").WithGuard(guard).WithData(collage.Load(
		func(ctx context.Context, rc *collage.RenderContext) (string, error) {
			user, _ := rc.Context().Value(rfeUserKey{}).(string)
			return user, nil
		})).Build()
	page := collage.NewPage("who").WithContent(who).WithPath("en", "/who").
		WithFragmentPath("en", "/who/live", who).Dynamic().Build()

	p := &rfePlugin{name: "test/live", middleware: []func(http.Handler) http.Handler{record("plugin-a")}}
	q := &rfePlugin{name: "test/second", middleware: []func(http.Handler) http.Handler{record("plugin-b")}}
	site := collagetest.NewSite(t, collagetest.WithPlugins(p, q),
		collagetest.WithTemplates(map[string]string{"who.html": `user={{.}}`}),
		collagetest.WithRegister(page),
		collagetest.WithSetup(func(app *collage.App) error { return app.Use(record("app")) }))

	site.Get("/who/live").WantStatus(http.StatusOK)
	mu.Lock()
	overHTTP := strings.Join(order, ",")
	order = nil
	mu.Unlock()

	post := httptest.NewRequest(http.MethodPost, "/_live/stream", strings.NewReader("a=b"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	got, err := p.host.RenderFragment(post, collage.FragmentRequest{Path: "/who/live"})
	if err != nil || rfeHTML(got) != "user=ada" {
		t.Fatalf("RenderFragment: %q, %v; want the middleware's user in the render", rfeHTML(got), err)
	}
	if guardSaw != "ada" {
		t.Errorf("the guard read user %q from the context, want ada", guardSaw)
	}
	mu.Lock()
	defer mu.Unlock()
	if through := strings.Join(order, ","); through != overHTTP {
		t.Errorf("middleware order through RenderFragment %q, over HTTP %q", through, overHTTP)
	}
}

// A middleware that answers without writing anything still answers: the
// fragment is refused, not rendered.
func TestRenderFragment_MiddlewareAnsweringSilently(t *testing.T) {
	silent := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/admin") {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	p := &rfePlugin{name: "test/live", middleware: []func(http.Handler) http.Handler{silent}}
	collagetest.NewSite(t, collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"stats.html": `<p>revenue</p>`}),
		collagetest.WithRegister(rfeStats()))
	got, err := p.host.RenderFragment(httptest.NewRequest(http.MethodGet, "/", nil), collage.FragmentRequest{Path: "/admin/stats"})
	if !errors.Is(err, collage.ErrGuardRefused) {
		t.Errorf("%q, %v; want ErrGuardRefused", rfeHTML(got), err)
	}
}

// A middleware that panics is an error, not a crash of the goroutine a plugin
// rendered from.
func TestRenderFragment_MiddlewarePanicIsError(t *testing.T) {
	boom := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/admin/stats" {
				panic("middleware panicked")
			}
			next.ServeHTTP(w, r)
		})
	}
	p := &rfePlugin{name: "test/live", middleware: []func(http.Handler) http.Handler{boom}}
	collagetest.NewSite(t, collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"stats.html": `<p>revenue</p>`}),
		collagetest.WithRegister(rfeStats()))
	got, err := p.host.RenderFragment(httptest.NewRequest(http.MethodGet, "/", nil), collage.FragmentRequest{Path: "/admin/stats"})
	if !errors.Is(err, collage.ErrPanic) || got != nil {
		t.Errorf("%q, %v; want ErrPanic", rfeHTML(got), err)
	}
}

// A middleware declaring a cache dimension with Vary does so without error, as
// over HTTP, and the render it declared one for is not Shared: it differs by
// more than its URL.
func TestRenderFragment_MiddlewareVary(t *testing.T) {
	var varyErr error
	var stream *http.Request
	vary := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/admin/stats" {
				varyErr = collage.Vary(r, "Accept-Language", "tr")
			}
			next.ServeHTTP(w, r)
			if r.URL.Path == "/" {
				stream = r
			}
		})
	}
	p := &rfePlugin{name: "test/live", middleware: []func(http.Handler) http.Handler{vary}}
	site := collagetest.NewSite(t, collagetest.WithPlugins(p),
		collagetest.WithTemplates(map[string]string{"stats.html": `<p>fixed</p>`}),
		collagetest.WithRegister(rfeStats()))

	// The caller is a request that went through the chain itself, as a stream
	// request has, so its own declarations are frozen.
	site.Get("/")
	if stream == nil {
		t.Fatal("no stream request captured")
	}
	got, err := p.host.RenderFragment(stream, collage.FragmentRequest{Path: "/admin/stats"})
	if err != nil {
		t.Fatalf("RenderFragment: %v", err)
	}
	if varyErr != nil {
		t.Errorf("Vary in middleware: %v", varyErr)
	}
	if got.Shared {
		t.Error("a render a middleware declared a Vary for is Shared")
	}
}

// HOOKS-01 / PLUGA-01 / PLUGA-B03: every PersonaliseHook rewrites what
// RenderFragment returns — its markup and what it hoisted — and a render a hook
// made personal is not Shared.
func TestRenderFragment_RunsPersonaliseHook(t *testing.T) {
	nonce := &rfeNonce{rfePlugin{name: "test/nonce"}}
	hoist := collage.Effect(func(_ context.Context, rc *collage.RenderContext) error {
		rc.Hoist("head", "script:go", template.HTML(`<script nonce="`+rfeMarker+`">head()</script>`))
		return nil
	})
	card := collage.NewFragment("card", "card.html").WithData(hoist).Build()
	page := collage.NewPage("cards").WithContent(card).WithPath("en", "/cards").
		WithFragmentPath("en", "/live/card", card).Incremental(time.Hour).Build()
	site := collagetest.NewSite(t, collagetest.WithPlugins(nonce),
		collagetest.WithConfig(func(c *collage.Config) {
			c.Cache = collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour}
		}),
		collagetest.WithTemplates(map[string]string{"card.html": `<script nonce="{{cspNonce}}">go()</script>`}),
		collagetest.WithRegister(page))
	if body := site.Get("/live/card").WantStatus(http.StatusOK).Body; strings.Contains(body, rfeMarker) {
		t.Fatalf("HTTP fragment read carries the marker: %q", body)
	}

	got, err := nonce.host.RenderFragment(httptest.NewRequest(http.MethodGet, "/", nil), collage.FragmentRequest{Path: "/live/card"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got.HTML), rfeMarker) || !strings.Contains(string(got.HTML), "nonce-xyz") {
		t.Errorf("HTML = %q; want the hook's nonce in place of the marker", got.HTML)
	}
	if len(got.Head) != 1 || strings.Contains(string(got.Head[0].HTML), rfeMarker) {
		t.Errorf("Head = %+v; want the hoisted script personalised too", got.Head)
	}
	if got.Shared {
		t.Error("a render a PersonaliseHook made personal is Shared")
	}
}

// HOOKS-07: once a hook sets Personal, a later hook cannot clear it.
func TestPersonalise_PersonalIsSticky(t *testing.T) {
	page := collage.NewPage("p").WithContent(collage.NewFragment("p", "p.html").Build()).
		WithPath("en", "/p").Incremental(time.Hour).Build()
	site := collagetest.NewSite(t,
		collagetest.WithPlugins(&rfeNonce{rfePlugin{name: "test/nonce"}}, &rfeLater{rfePlugin{name: "test/later"}}),
		collagetest.WithConfig(func(c *collage.Config) {
			c.Cache = collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour}
		}),
		collagetest.WithTemplates(map[string]string{"p.html": `<script nonce="{{cspNonce}}">go()</script>`}),
		collagetest.WithRegister(page))
	first := site.Get("/p").WantStatus(http.StatusOK)
	if !strings.Contains(first.Body, "nonce-xyz") {
		t.Fatalf("body %q", first.Body)
	}
	if cc := first.Header.Get("Cache-Control"); !strings.Contains(cc, "private") {
		t.Errorf("Cache-Control %q; a later hook cleared Personal", cc)
	}
	req := site.Request(http.MethodGet, "/p", nil)
	req.Header.Set("If-None-Match", first.Header.Get("ETag"))
	if res := site.Do(req); res.Status == http.StatusNotModified {
		t.Error("a personal body was answered 304")
	}
}

// rfeEarly renders from its own Init, before the middleware chain exists.
type rfeEarly struct {
	rfePlugin
	err error
}

func (p *rfeEarly) Init(_ context.Context, h collage.Host) error {
	_, p.err = h.RenderFragment(httptest.NewRequest(http.MethodGet, "/", nil), collage.FragmentRequest{Path: "/admin/stats"})
	return nil
}

// A render asked for while the application is starting is refused rather than
// made without the middleware, or waited for until a start that is waiting on
// it finishes.
func TestRenderFragment_DuringStartIsRefused(t *testing.T) {
	early := &rfeEarly{rfePlugin: rfePlugin{name: "test/early"}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		collagetest.NewSite(t, collagetest.WithPlugins(early, &rfePlugin{name: "test/auth", middleware: []func(http.Handler) http.Handler{rfeBasicAuth("/admin")}}),
			collagetest.WithTemplates(map[string]string{"stats.html": `<p>revenue</p>`}),
			collagetest.WithRegister(rfeStats()))
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RenderFragment from Init deadlocked the start")
	}
	if early.err == nil {
		t.Error("RenderFragment from Init rendered before the middleware chain existed")
	}
}

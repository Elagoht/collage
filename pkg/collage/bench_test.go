package collage_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// These measure what one request costs on each path a site serves: a page from
// the cache, one rendered for its reader, one with many parts, a form's POST, and
// the requests nobody wants — a 404 and a probe refused before routing. They are
// the line a later change is compared against, not a claim about any one number.

// benchApp is nextjsApp for a benchmark: inline fragments only, the memory cache
// on, and the forgery check on when csrf is set.
func benchApp(b *testing.B, csrf bool) *collage.App {
	return benchAppWith(b, csrf, fstest.MapFS{"t/keep.html": {Data: []byte(`x`)}})
}

func benchAppWith(b *testing.B, csrf bool, files fstest.MapFS) *collage.App {
	b.Helper()
	cfg := &collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 0},
		Template: collage.TemplateConfig{FS: files, Root: "t"},
		Cache:    collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
	}
	if csrf {
		cfg.Security = collage.SecurityConfig{CSRFKey: bytes.Repeat([]byte("k"), 32)}
	}
	app, err := collage.New(cfg)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
	})
	return app
}

func benchRegister(b *testing.B, app *collage.App, pages ...*collage.Page) {
	b.Helper()
	for _, p := range pages {
		if err := app.RegisterPage(p); err != nil {
			b.Fatalf("RegisterPage %q: %v", p.Name, err)
		}
	}
}

// benchServe runs req through h b.N times and fails on the first answer that is
// not want, so a benchmark never times an error page by mistake.
func benchServe(b *testing.B, h http.Handler, want int, req func(i int) *http.Request) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req(i))
		if rec.Code != want {
			b.Fatalf("request %d: status %d, want %d: %s", i, rec.Code, want, rec.Body.String())
		}
	}
}

func benchGet(path string) func(int) *http.Request {
	return func(int) *http.Request { return httptest.NewRequest(http.MethodGet, path, nil) }
}

// greeting is a fragment whose handler the strategy cannot see through, as most
// real fragments are.
func greeting(name string) *collage.Fragment {
	return collage.NewInlineFragment(name, `<p>{{.}}</p>`).
		WithDataHandler(collage.Load(func(_ context.Context, _ *collage.RenderContext) (string, error) {
			return "hello", nil
		})).Build()
}

// withParts is a page content of n child fragments, each in a slot of its own.
func withParts(name string, n int) *collage.Fragment {
	var html strings.Builder
	html.WriteString("<main>")
	for i := range n {
		fmt.Fprintf(&html, `{{slot "s%d"}}`, i)
	}
	html.WriteString("</main>")
	fb := collage.NewInlineFragment(name, html.String())
	for i := range n {
		fb = fb.WithSlotFragment("s"+strconv.Itoa(i), greeting(fmt.Sprintf("%s-%d", name, i)))
	}
	return fb.Build()
}

func BenchmarkPage_StaticHit(b *testing.B) {
	app := benchApp(b, false)
	benchRegister(b, app, collage.NewPage("p").WithContent(greeting("g")).WithPath("en", "/").Static().Build())
	h := app.Handler()
	benchServe(b, h, http.StatusOK, benchGet("/"))
}

func BenchmarkPage_IncrementalHit(b *testing.B) {
	app := benchApp(b, false)
	benchRegister(b, app, collage.NewPage("p").WithContent(greeting("g")).WithPath("en", "/").Incremental(time.Hour).Build())
	h := app.Handler()
	benchServe(b, h, http.StatusOK, benchGet("/"))
}

// Every request names a page the cache has not seen, so each one renders, writes
// the cache and, past its capacity, evicts.
func BenchmarkPage_IncrementalMiss(b *testing.B) {
	app := benchApp(b, false)
	benchRegister(b, app, collage.NewPage("p").WithContent(greeting("g")).WithPath("en", "/").
		Incremental(time.Hour).WithCacheParams("n").Build())
	h := app.Handler()
	benchServe(b, h, http.StatusOK, func(i int) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/?n="+strconv.Itoa(i), nil)
	})
}

func BenchmarkPage_Dynamic(b *testing.B) {
	app := benchApp(b, false)
	benchRegister(b, app, collage.NewPage("p").WithContent(greeting("g")).WithPath("en", "/").Dynamic().Build())
	h := app.Handler()
	benchServe(b, h, http.StatusOK, benchGet("/"))
}

func BenchmarkPage_DynamicParts(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			app := benchApp(b, false)
			benchRegister(b, app, collage.NewPage("p").WithContent(withParts("c", n)).WithPath("en", "/").Dynamic().Build())
			h := app.Handler()
			benchServe(b, h, http.StatusOK, benchGet("/"))
		})
	}
}

// A real project holds many templates and a page renders a few of them. What a
// render costs must follow the page, not the size of the project around it.
func BenchmarkPage_LargeProject(b *testing.B) {
	for _, templates := range []int{1, 200} {
		b.Run(strconv.Itoa(templates)+"-templates", func(b *testing.B) {
			files := fstest.MapFS{}
			for i := range templates {
				files[fmt.Sprintf("t/unused/%d.html", i)] = &fstest.MapFile{
					Data: []byte(`<section><h2>{{.}}</h2>{{if .}}<p>{{.}}</p>{{end}}<a href="/x?q={{.}}">link</a></section>`),
				}
			}
			app := benchAppWith(b, false, files)
			benchRegister(b, app, collage.NewPage("p").WithContent(withParts("c", 5)).WithPath("en", "/").Dynamic().Build())
			h := app.Handler()
			benchServe(b, h, http.StatusOK, benchGet("/"))
		})
	}
}

func BenchmarkPage_FragmentPath(b *testing.B) {
	app := benchApp(b, false)
	part := greeting("part")
	benchRegister(b, app, collage.NewPage("p").WithContent(withParts("c", 1)).WithPath("en", "/").
		WithFragmentPath("en", "/part", part).Dynamic().Build())
	h := app.Handler()
	benchServe(b, h, http.StatusOK, benchGet("/part"))
}

// A page cached once per value of a header middleware declared: the cost the
// extra key dimension and the Varied read add to a hit.
func BenchmarkPage_VariedHit(b *testing.B) {
	app := benchApp(b, false)
	if err := app.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = collage.Vary(r, "Accept-Language", r.Header.Get("Accept-Language"))
			next.ServeHTTP(w, r)
		})
	}); err != nil {
		b.Fatal(err)
	}
	lang := collage.NewInlineFragment("lang", `<p>{{.}}</p>`).
		WithDataHandler(collage.Load(func(_ context.Context, rc *collage.RenderContext) (string, error) {
			v, _ := collage.Varied(rc, "Accept-Language")
			return v, nil
		})).Build()
	benchRegister(b, app, collage.NewPage("p").WithContent(lang).WithPath("en", "/").Incremental(time.Hour).Build())
	h := app.Handler()
	langs := []string{"en", "tr", "de", "fr"}
	benchServe(b, h, http.StatusOK, func(i int) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Language", langs[i%len(langs)])
		return r
	})
}

func BenchmarkPage_Guarded(b *testing.B) {
	app := benchApp(b, false)
	private := collage.NewInlineFragment("private", `<div>{{slot "content"}}</div>`).
		WithGuard(func(_ context.Context, r *http.Request) (*collage.GuardDecision, error) {
			if r.Header.Get("X-User") != "" {
				return nil, nil
			}
			return &collage.GuardDecision{Status: http.StatusSeeOther, Location: "/login"}, nil
		}).Build()
	benchRegister(b, app, collage.NewPage("p").WithLayouts(private).WithContent(greeting("g")).WithPath("en", "/").Dynamic().Build())
	h := app.Handler()
	b.Run("allowed", func(b *testing.B) {
		benchServe(b, h, http.StatusOK, func(int) *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("X-User", "u")
			return r
		})
	})
	b.Run("refused", func(b *testing.B) {
		benchServe(b, h, http.StatusSeeOther, benchGet("/"))
	})
}

// A form's POST with the forgery check on: the token cookie and field verified,
// the form parsed, the handler's redirect written.
func BenchmarkAction_CSRFPost(b *testing.B) {
	app := benchApp(b, true)
	form := collage.NewInlineFragment("form", `<form method="post">{{csrfToken}}</form>`).Build()
	benchRegister(b, app, collage.NewPage("p").WithContent(form).WithPath("en", "/form").Dynamic().
		WithAction(http.MethodPost, func(_ context.Context, _ *collage.RenderContext) (*collage.ActionResult, error) {
			return collage.SeeOther("/thanks"), nil
		}).Build())
	h := app.Handler()

	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/form", nil))
	var token *http.Cookie
	for _, c := range page.Result().Cookies() {
		if c.Name == "collage_csrf" {
			token = c
		}
	}
	if token == nil {
		b.Fatalf("the form set no token cookie: %v", page.Header().Values("Set-Cookie"))
	}
	body := url.Values{"_csrf": {token.Value}, "message": {"hello"}}.Encode()
	benchServe(b, h, http.StatusSeeOther, func(int) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/form", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.AddCookie(token)
		return r
	})
}

// What the traffic a site does not serve costs it: a path no route matches, and
// an encoded slash refused before routing.
func BenchmarkUnwanted(b *testing.B) {
	app := benchApp(b, false)
	benchRegister(b, app, collage.NewPage("p").WithContent(greeting("g")).WithPath("en", "/").Static().Build())
	h := app.Handler()
	b.Run("not-found", func(b *testing.B) {
		benchServe(b, h, http.StatusNotFound, benchGet("/no/such/page"))
	})
	b.Run("encoded-slash", func(b *testing.B) {
		benchServe(b, h, http.StatusNotFound, benchGet("/a%2fb"))
	})
}

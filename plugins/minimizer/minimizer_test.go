package minimizer_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"

	"example.com/minimizer"
)

// templates is the smallest site that can show minification happening: a layout
// with enough whitespace to be worth removing.
var templates = fstest.MapFS{
	"layouts/main.html": &fstest.MapFile{Data: []byte(
		"<!DOCTYPE html>\n<html>\n  <head>\n    <!-- a comment -->\n    <title>t</title>\n  </head>\n  <body>\n    {{slot \"content\"}}\n  </body>\n</html>\n")},
	"pages/home.html": &fstest.MapFile{Data: []byte("<p>\n      <b>Hello</b>   world\n    </p>\n")},
}

func newSite(t *testing.T, plugins ...collage.Plugin) http.Handler {
	t.Helper()

	app, err := collage.New(&collage.Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Template: collage.TemplateConfig{FS: templates, Extension: ".html"},
		Plugins:  plugins,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	layout := collage.NewFragment("layout", "layouts/main.html").WithSlot("content", true, false).Build()
	home := collage.NewPage("home").
		WithLayout(layout).
		WithContent(collage.NewFragment("home", "pages/home.html").Build()).
		WithPath("en", "/").
		Build()
	if err := app.RegisterPage(home); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	doc := collage.NewDocument("data", "application/json; charset=utf-8").
		WithPath("en", "/data.json").
		WithHandler(func(context.Context, *collage.RenderContext) ([]byte, []string, error) {
			return []byte("{\n  \"a\": 1,\n  \"b\": [1, 2]\n}\n"), nil, nil
		}).
		Dynamic().
		Build()
	if err := app.RegisterDocument(doc); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}

	assets := fstest.MapFS{
		"app.css": &fstest.MapFile{Data: []byte(".a {\n  /* note */\n  color: red;\n}\n")},
		"app.js":  &fstest.MapFile{Data: []byte("function f() {\n    // note\n    return 1\n}\n")},
	}
	if err := app.Mount("/static/", assets); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app.Handler()
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", target, rec.Code, rec.Body.String())
	}
	return rec
}

func TestPlugin_MinifiesPagesDocumentsAndAssets(t *testing.T) {
	plain := newSite(t)
	small := newSite(t, minimizer.NewWith(minimizer.Config{HTML: true, JSON: true, CSS: true, JS: true}))

	for _, target := range []string{"/", "/data.json", "/static/app.css", "/static/app.js"} {
		before := get(t, plain, target).Body.Len()
		after := get(t, small, target).Body.Len()
		if after >= before {
			t.Errorf("GET %s: %d bytes with the plugin, %d without — nothing was saved", target, after, before)
		}
	}
}

func TestPlugin_MinifiedPageIsStillTheSamePage(t *testing.T) {
	site := newSite(t, minimizer.New())

	body := get(t, site, "/").Body.String()
	if !strings.Contains(body, "</b> world") {
		t.Errorf("the separating space was lost, joining two words: %q", body)
	}
	if strings.Contains(body, "a comment") {
		t.Errorf("the comment survived: %q", body)
	}
	if !strings.Contains(body, "<title>t</title>") {
		t.Errorf("the page lost its title: %q", body)
	}
}

func TestPlugin_MinifiedDocumentIsStillValidJSON(t *testing.T) {
	site := newSite(t, minimizer.New())

	body := get(t, site, "/data.json").Body.Bytes()
	var decoded map[string]any // any: what encoding/json decodes into
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("minified document is not valid JSON: %v (%s)", err, body)
	}
	if decoded["a"] != float64(1) {
		t.Errorf("value changed: %v", decoded)
	}
}

func TestPlugin_SuffixRangeAddressesTheMinifiedBytes(t *testing.T) {
	// A suffix range is answered by seeking to length-N, so it only produces the
	// right bytes if the length the server works from describes what it serves.
	// The mount hands ServeContent an io.ReadSeeker over the minified content, so
	// it does — this pins that down from the outside, where it is observable.
	site := newSite(t, minimizer.New())

	full := get(t, site, "/static/app.css").Body.String()
	if len(full) < 4 {
		t.Fatalf("minified stylesheet is too small to range over: %q", full)
	}

	req := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	req.Header.Set("Range", "bytes=-4")
	rec := httptest.NewRecorder()
	site.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got, want := rec.Body.String(), full[len(full)-4:]; got != want {
		t.Errorf("last four bytes = %q, want %q — the advertised size does not describe what is served", got, want)
	}
	if got := rec.Header().Get("Content-Range"); !strings.HasSuffix(got, "/"+itoa(len(full))) {
		t.Errorf("Content-Range = %q, want it to end in the minified length %d", got, len(full))
	}
}

func TestPlugin_RangeRequestsStillWorkOnAMinifiedAsset(t *testing.T) {
	// The reason minification wraps the filesystem rather than the response: a
	// transformation applied per request shifts every byte offset, and a range
	// request then returns the wrong slice.
	site := newSite(t, minimizer.New())

	full := get(t, site, "/static/app.css").Body.String()

	req := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	req.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()
	site.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got, want := rec.Body.String(), full[:4]; got != want {
		t.Errorf("range body = %q, want %q — the offsets do not describe what is served", got, want)
	}
}

func TestPlugin_DisabledFormatsAreLeftAlone(t *testing.T) {
	site := newSite(t, minimizer.NewWith(minimizer.Config{HTML: true}))
	plain := newSite(t)

	if got, want := get(t, site, "/static/app.css").Body.String(), get(t, plain, "/static/app.css").Body.String(); got != want {
		t.Errorf("CSS was minified although it is disabled:\n got  %q\n want %q", got, want)
	}
	if get(t, site, "/").Body.Len() >= get(t, plain, "/").Body.Len() {
		t.Error("HTML was not minified although it is enabled")
	}
}

func TestPlugin_ConfigComesFromTheApplication(t *testing.T) {
	p := minimizer.NewWith(minimizer.Config{})
	app, err := collage.New(&collage.Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Template: collage.TemplateConfig{FS: templates, Extension: ".html"},
		Plugins:  []collage.Plugin{p},
		PluginConfig: map[string]json.RawMessage{
			minimizer.Name: json.RawMessage(`{"html":true,"css":false}`),
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if app == nil {
		t.Fatal("New returned no application")
	}
	// Proven through behaviour rather than a getter: the plugin exposes no
	// accessor, and one added only for a test would be surface nobody else wants.
}

func TestPlugin_RejectedByRegisterPlugin(t *testing.T) {
	// It wraps mounted filesystems, which happens while the application is built,
	// so it must arrive through Config.Plugins. Being skipped silently would mean
	// assets quietly stopped being minified.
	app, err := collage.New(&collage.Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Template: collage.TemplateConfig{FS: templates, Extension: ".html"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.RegisterPlugin(minimizer.New()); err == nil {
		t.Fatal("RegisterPlugin accepted a Configurer, which would silently skip Configure")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

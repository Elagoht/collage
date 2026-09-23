package jsonld_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"

	"example.com/jsonld"
)

var templates = fstest.MapFS{
	"layouts/main.html": &fstest.MapFile{Data: []byte(
		`<!DOCTYPE html><html><head><title>t</title></head><body>{{slot "content"}}</body></html>`)},
	"pages/article.html": &fstest.MapFile{Data: []byte(`<article>{{.Headline}}</article>`)},
	"pages/bare.html":    &fstest.MapFile{Data: []byte(`<p>no head here</p>`)},
}

type articleView struct{ Headline string }

// newSite builds a site whose article page emits whatever emit returns.
func newSite(t *testing.T, p collage.Plugin, emit func(*collage.RenderContext)) http.Handler {
	t.Helper()

	app, err := collage.New(&collage.Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Template: collage.TemplateConfig{FS: templates, Extension: ".html"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p != nil {
		if err := app.RegisterPlugin(p); err != nil {
			t.Fatalf("RegisterPlugin: %v", err)
		}
	}

	layout := collage.NewFragment("layout", "layouts/main.html").WithSlot("content", true, false).Build()
	article := collage.NewPage("article").
		WithLayout(layout).
		WithContent(collage.NewFragment("article", "pages/article.html").
			WithDataHandler(func(_ context.Context, rc *collage.RenderContext) (any, []string, error) { // any: the framework's own handler signature
				if emit != nil {
					emit(rc)
				}
				return articleView{Headline: "A Headline"}, nil, nil
			}).
			Build()).
		WithPath("en", "/article").
		Build()
	if err := app.RegisterPage(article); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	// A page with no <head> at all, to show the plugin declining rather than
	// inventing a place to write.
	bare := collage.NewPage("bare").
		WithContent(collage.NewFragment("bare", "pages/bare.html").
			WithDataHandler(func(_ context.Context, rc *collage.RenderContext) (any, []string, error) { // any: the framework's own handler signature
				jsonld.Emit(rc, jsonld.Article{Headline: "unreachable"})
				return nil, nil, nil
			}).
			Build()).
		WithPath("en", "/bare").
		Build()
	if err := app.RegisterPage(bare); err != nil {
		t.Fatalf("RegisterPage(bare): %v", err)
	}

	return app.Handler()
}

var scriptBlock = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)

// blocks returns the decoded JSON-LD nodes on a page, failing the test if any of
// them does not parse — which is the whole point of emitting them.
func blocks(t *testing.T, body string) []map[string]any { // any: what encoding/json decodes into
	t.Helper()
	var out []map[string]any // any: what encoding/json decodes into
	for _, m := range scriptBlock.FindAllStringSubmatch(body, -1) {
		var node map[string]any // any: what encoding/json decodes into
		if err := json.Unmarshal([]byte(m[1]), &node); err != nil {
			t.Fatalf("a JSON-LD block does not parse: %v\n%s", err, m[1])
		}
		out = append(out, node)
	}
	return out
}

func get(t *testing.T, h http.Handler, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestPlugin_EmitsWhatTheRenderContributed(t *testing.T) {
	published := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	site := newSite(t, jsonld.New(), func(rc *collage.RenderContext) {
		jsonld.Emit(rc, jsonld.Article{
			Headline:      "Seawalls Buy Time",
			DatePublished: published,
			AuthorName:    "Noor Haddad",
			Keywords:      []string{"climate", "adaptation"},
		})
	})

	nodes := blocks(t, get(t, site, "/article"))
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(nodes))
	}
	node := nodes[0]
	if node["@type"] != "Article" || node["@context"] != "https://schema.org" {
		t.Errorf("node = %v, want a schema.org Article", node)
	}
	if node["headline"] != "Seawalls Buy Time" {
		t.Errorf("headline = %v", node["headline"])
	}
	if node["datePublished"] != published.Format(time.RFC3339) {
		t.Errorf("datePublished = %v, want RFC3339", node["datePublished"])
	}
	author, ok := node["author"].(map[string]any) // any: what encoding/json decodes into
	if !ok || author["name"] != "Noor Haddad" {
		t.Errorf("author = %v", node["author"])
	}
}

func TestPlugin_EmitsIntoTheHead(t *testing.T) {
	site := newSite(t, jsonld.New(), func(rc *collage.RenderContext) {
		jsonld.Emit(rc, jsonld.Article{Headline: "x"})
	})

	body := get(t, site, "/article")
	head := strings.Index(body, "</head>")
	script := strings.Index(body, `<script type="application/ld+json">`)
	if script < 0 || script > head {
		t.Errorf("the block is not inside <head>: script at %d, </head> at %d", script, head)
	}
}

func TestPlugin_ClosingScriptTagInContentCannotEscape(t *testing.T) {
	// The HTML parser ends a script element at the first "</script" in its text,
	// wherever it appears — including inside a JSON string. A headline containing
	// one would close the block early and spill the rest of the JSON into the page
	// as markup: an injection, not a formatting bug.
	//
	// What prevents it is that json.Marshal escapes "<", ">" and "&" by default.
	// This test is what stands between that default and someone reaching for an
	// Encoder with SetEscapeHTML(false), which produces valid JSON and this hole.
	site := newSite(t, jsonld.New(), func(rc *collage.RenderContext) {
		jsonld.Emit(rc, jsonld.Article{Headline: `</script><img src=x onerror=alert(1)>`})
	})

	body := get(t, site, "/article")

	if strings.Contains(body, "<img src=x") {
		t.Fatalf("the payload escaped the script block:\n%s", body)
	}
	nodes := blocks(t, body)
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(nodes))
	}
	// Escaped, and still the same string once parsed.
	if nodes[0]["headline"] != `</script><img src=x onerror=alert(1)>` {
		t.Errorf("headline decoded to %q, want the original", nodes[0]["headline"])
	}
}

func TestPlugin_EmitsNothingWhenThereIsNothingToSay(t *testing.T) {
	// An empty block is worse than no block: a consumer that fails to parse one
	// may distrust the whole page.
	site := newSite(t, jsonld.New(), nil)

	if body := get(t, site, "/article"); strings.Contains(body, "application/ld+json") {
		t.Errorf("a script block was emitted for a page with no data:\n%s", body)
	}
}

func TestPlugin_DeclinesAPageWithNoHead(t *testing.T) {
	site := newSite(t, jsonld.New(), nil)

	if body := get(t, site, "/bare"); strings.Contains(body, "application/ld+json") {
		t.Errorf("data was written into a page with no head:\n%s", body)
	}
}

func TestPlugin_AddsTheConfiguredWebSiteNode(t *testing.T) {
	site := newSite(t, jsonld.NewWith(jsonld.Config{
		SiteName:  "The Wire",
		SiteURL:   "https://thewire.example",
		SearchURL: "https://thewire.example/search?q={query}",
	}), func(rc *collage.RenderContext) {
		jsonld.Emit(rc, jsonld.Article{Headline: "x"})
	})

	nodes := blocks(t, get(t, site, "/article"))
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want the article and the site", len(nodes))
	}

	var found bool
	for _, node := range nodes {
		if node["@type"] != "WebSite" {
			continue
		}
		found = true
		if node["name"] != "The Wire" {
			t.Errorf("site name = %v", node["name"])
		}
		action, ok := node["potentialAction"].(map[string]any) // any: what encoding/json decodes into
		if !ok || action["@type"] != "SearchAction" {
			t.Errorf("potentialAction = %v, want a SearchAction", node["potentialAction"])
		}
	}
	if !found {
		t.Error("no WebSite node was emitted")
	}
}

func TestPlugin_DoesNotDuplicateAPagesOwnWebSiteNode(t *testing.T) {
	site := newSite(t, jsonld.NewWith(jsonld.Config{SiteName: "The Wire"}), func(rc *collage.RenderContext) {
		jsonld.Emit(rc, jsonld.WebSite{Name: "Something Else"})
	})

	nodes := blocks(t, get(t, site, "/article"))
	if len(nodes) != 1 || nodes[0]["name"] != "Something Else" {
		t.Errorf("nodes = %v, want only the page's own WebSite", nodes)
	}
}

func TestPlugin_Disabled(t *testing.T) {
	site := newSite(t, jsonld.NewWith(jsonld.Config{SiteName: "The Wire", Disabled: true}), func(rc *collage.RenderContext) {
		jsonld.Emit(rc, jsonld.Article{Headline: "x"})
	})

	if body := get(t, site, "/article"); strings.Contains(body, "application/ld+json") {
		t.Error("data was emitted although the plugin is disabled")
	}
}

func TestPlugin_MultipleFragmentsEachContribute(t *testing.T) {
	// Emit appends, so two fragments can contribute without knowing about each
	// other — which is the only way a breadcrumb fragment and a content fragment
	// can both have a say.
	site := newSite(t, jsonld.New(), func(rc *collage.RenderContext) {
		jsonld.Emit(rc, jsonld.Article{Headline: "x"})
		jsonld.Emit(rc, jsonld.BreadcrumbList{Items: []jsonld.Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Climate", URL: "/category/climate"},
		}})
	})

	nodes := blocks(t, get(t, site, "/article"))
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}
	for _, node := range nodes {
		if node["@type"] != "BreadcrumbList" {
			continue
		}
		items, ok := node["itemListElement"].([]any) // any: what encoding/json decodes into
		if !ok || len(items) != 2 {
			t.Fatalf("itemListElement = %v", node["itemListElement"])
		}
		first, _ := items[0].(map[string]any) // any: what encoding/json decodes into
		if first["position"] != float64(1) {
			t.Errorf("first position = %v, want 1 — schema.org positions are 1-based", first["position"])
		}
	}
}

func TestRaw_RejectsInvalidJSON(t *testing.T) {
	// Invalid JSON inside a script tag is worse than absent JSON: a consumer that
	// fails to parse it may discard every other node on the page with it.
	if _, err := json.Marshal(jsonld.Raw{SchemaType: "Event", JSON: []byte(`{"broken"`)}); err == nil {
		t.Fatal("Marshal accepted invalid JSON")
	}
}

func TestRaw_AddsTheContext(t *testing.T) {
	out, err := json.Marshal(jsonld.Raw{SchemaType: "Event", JSON: []byte(`{"@type":"Event","name":"x"}`)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var node map[string]any // any: what encoding/json decodes into
	if err := json.Unmarshal(out, &node); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if node["@context"] != "https://schema.org" {
		t.Errorf("@context = %v", node["@context"])
	}
}

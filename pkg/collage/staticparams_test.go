package collage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// staticBlog is a blog in two locales: a post page at "/blog/{slug}" whose handler
// renders the slug and locale it was reached with, and whose StaticParams lists a
// locale's posts.
func staticBlog(t *testing.T, posts map[string][]string) (*App, *Page) {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"layouts/default.html": `<main>{{slot "content"}}</main>`,
		"pages/post.html":      `<h1>{{.}}</h1>`,
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	app, err := New(&Config{
		Template: TemplateConfig{Root: root},
		Locale:   LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	content := NewFragment("post", "pages/post.html").
		WithDataHandler(Load(func(_ context.Context, rc *RenderContext) (string, error) {
			return rc.Locale + ":" + rc.Param("slug"), nil
		})).
		Build()
	page := NewPage("post").
		WithLayout(NewFragment("layout", "layouts/default.html").Build()).
		WithContent(content).
		WithPath("en", "/blog/{slug}").
		WithPath("tr", "/yazi/{slug}").
		Static().
		WithStaticParams(func(_ context.Context, locale string) ([]map[string]string, error) {
			var params []map[string]string
			for _, slug := range posts[locale] {
				params = append(params, map[string]string{"slug": slug})
			}
			return params, nil
		}).
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	return app, page
}

func buildInto(t *testing.T, app *App) (string, *BuildReport, error) {
	t.Helper()
	out := t.TempDir()
	builder, err := NewBuilder(app, BuildOptions{OutDir: out})
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	report, err := builder.Build(context.Background())
	return out, report, err
}

func readBuilt(t *testing.T, out, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(body)
}

// TestWithStaticParams_BuildsEveryListedPost is the static blog: every post listed
// is written, in every locale, under that locale's prefix, rendered with the slug
// and locale a request to it would carry.
func TestWithStaticParams_BuildsEveryListedPost(t *testing.T) {
	app, _ := staticBlog(t, map[string][]string{
		"en": {"hello", "world"},
		"tr": {"merhaba", "héllo"},
	})
	out, report, err := buildInto(t, app)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for rel, want := range map[string]string{
		"blog/hello/index.html":      "<h1>en:hello</h1>",
		"blog/world/index.html":      "<h1>en:world</h1>",
		"tr/yazi/merhaba/index.html": "<h1>tr:merhaba</h1>",
		// Written at its decoded path: a static host looks a request for
		// /tr/yazi/h%C3%A9llo up as tr/yazi/héllo.
		"tr/yazi/héllo/index.html": "<h1>tr:héllo</h1>",
	} {
		if got := readBuilt(t, out, rel); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want it to contain %q", rel, got, want)
		}
	}
	if len(report.Written) != 4 || len(report.Skipped) != 0 {
		t.Errorf("written %v, skipped %v; want four written and nothing skipped", report.Written, report.Skipped)
	}
}

// TestWithStaticParams_ValuesThatDoNotFitFailThatFileAlone: a set missing the
// pattern's name, or carrying one it does not have, is an error against that file,
// and the rest of the list is still built.
func TestWithStaticParams_ValuesThatDoNotFitFailThatFileAlone(t *testing.T) {
	app, page := staticBlog(t, nil)
	page.StaticParams = func(_ context.Context, locale string) ([]map[string]string, error) {
		if locale != "en" {
			return nil, nil
		}
		return []map[string]string{{"slug": "fine"}, {"id": "wrong"}, {"slug": "fine-too", "extra": "x"}}, nil
	}
	out, report, err := buildInto(t, app)
	if !errors.Is(err, ErrRouteParams) {
		t.Fatalf("Build = %v, want ErrRouteParams", err)
	}
	if len(report.Errors) != 2 {
		t.Errorf("Errors = %v, want one per set that does not fit", report.Errors)
	}
	if got := readBuilt(t, out, "blog/fine/index.html"); !strings.Contains(got, "en:fine") {
		t.Errorf("the set that fits was not built: %q", got)
	}
}

// TestWithStaticParams_ListFailures: an error from the list, or a panic in it, fails
// that route's locale and names it, and the build carries on with the rest.
func TestWithStaticParams_ListFailures(t *testing.T) {
	failure := errors.New("cms down")
	for name, list := range map[string]StaticParamsFunc{
		"error": func(context.Context, string) ([]map[string]string, error) { return nil, failure },
		"panic": func(context.Context, string) ([]map[string]string, error) { panic("boom") },
	} {
		t.Run(name, func(t *testing.T) {
			app, page := staticBlog(t, nil)
			page.StaticParams = list
			_, report, err := buildInto(t, app)
			if err == nil || !strings.Contains(err.Error(), `page "post" locale "en"`) {
				t.Fatalf("Build = %v, want an error naming the page and locale", err)
			}
			if name == "error" && !errors.Is(err, failure) {
				t.Errorf("Build = %v, want the list's own error wrapped", err)
			}
			if name == "panic" && !errors.Is(err, ErrBuildPanic) {
				t.Errorf("Build = %v, want ErrBuildPanic", err)
			}
			if len(report.Errors) != 2 {
				t.Errorf("Errors = %v, want one per locale", report.Errors)
			}
		})
	}
}

// TestWithStaticParams_Documents: a document's pattern expands the same way, and is
// written to its literal path.
func TestWithStaticParams_Documents(t *testing.T) {
	app, _ := staticBlog(t, nil)
	doc := NewDocument("feed", "application/rss+xml").
		WithPath("en", "/feeds/{category}/rss.xml").
		WithHandler(func(_ context.Context, rc *RenderContext) ([]byte, []string, error) {
			return []byte("<rss>" + rc.Param("category") + "</rss>"), nil, nil
		}).
		Static().
		WithStaticParams(func(context.Context, string) ([]map[string]string, error) {
			return []map[string]string{{"category": "go"}, {"category": "web"}}, nil
		}).
		Build()
	if err := app.RegisterDocument(doc); err != nil {
		t.Fatalf("RegisterDocument: %v", err)
	}
	out, _, err := buildInto(t, app)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := readBuilt(t, out, "feeds/web/rss.xml"); got != "<rss>web</rss>" {
		t.Errorf("feeds/web/rss.xml = %q", got)
	}
}

package collage

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestPageBuilder_MinimalApp mirrors the page-building portion of
// docs/spec/usage-examples.md's "Minimal application" example verbatim: a
// layout-wrapped home page with one localized path and incremental caching.
func TestPageBuilder_MinimalApp(t *testing.T) {
	layout := NewFragment("layout", "layouts/default.html").
		WithSlot("content", true, false).
		Build()

	homeContent := NewFragment("home-content", "pages/home.html").
		WithDataHandler(func(ctx context.Context, rc *RenderContext) (any, []string, error) {
			return map[string]any{"Title": "Welcome Home"}, []string{"homepage"}, nil
		}).
		Build()

	homePage := NewPage("home").
		WithLayout(layout).
		WithContent(homeContent).
		WithPath("en", "/").
		Incremental(5 * time.Minute).
		Build()

	if homePage.Name != "home" {
		t.Errorf("Name = %q, want %q", homePage.Name, "home")
	}
	if homePage.LayoutFragment != layout {
		t.Errorf("LayoutFragment = %v, want %v", homePage.LayoutFragment, layout)
	}
	if homePage.ContentFragment != homeContent {
		t.Errorf("ContentFragment = %v, want %v", homePage.ContentFragment, homeContent)
	}
	if got, ok := homePage.PathFor("en"); !ok || got != "/" {
		t.Errorf("PathFor(en) = (%q, %v), want (\"/\", true)", got, ok)
	}
	if homePage.Strategy != StrategyIncremental {
		t.Errorf("Strategy = %v, want StrategyIncremental", homePage.Strategy)
	}
	if homePage.CacheTTL != 5*time.Minute {
		t.Errorf("CacheTTL = %v, want 5m", homePage.CacheTTL)
	}
}

// TestPageBuilder_BlogExample mirrors docs/spec/usage-examples.md's "Advanced
// example: redirects and custom error pages" verbatim: a blog post page wrapped in
// the shared layout, two localized paths, a permanent (301) and a temporary (302)
// redirect, page-specific 404/500 pages, incremental caching, and a dependency tag —
// plus the two global error pages the example builds alongside it.
func TestPageBuilder_BlogExample(t *testing.T) {
	layout := NewFragment("layout", "layouts/default.html").
		WithSlot("content", true, false).
		Build()

	blog404Content := NewFragment("blog-404", "errors/blog-404.html").
		Build()

	blog404Page := NewPage("blog-404").
		WithLayout(layout).
		WithContent(blog404Content).
		Build()

	blog500Content := NewFragment("blog-500", "errors/blog-500.html").
		Build()

	blog500Page := NewPage("blog-500").
		WithLayout(layout).
		WithContent(blog500Content).
		Build()

	fetchPost := func(slug string) (*fetchedPost, error) {
		if slug == "" {
			return nil, errors.New("post not found")
		}
		return &fetchedPost{Slug: slug}, nil
	}

	blogPostContent := NewFragment("blog-post", "pages/blog-post.html").
		WithDataHandler(func(ctx context.Context, rc *RenderContext) (any, []string, error) {
			slug := rc.PathParams["slug"]
			post, err := fetchPost(slug)
			if err != nil {
				return nil, nil, err // Will trigger custom 500 page
			}
			return post, []string{fmt.Sprintf("post:%s", slug)}, nil
		}).
		Required().
		Build()

	blogPostPage := NewPage("blog-post").
		WithLayout(layout).
		WithContent(blogPostContent).
		WithPath("en", "/blog/{slug}").
		WithPath("tr", "/blog/{slug}").
		WithRedirect("/old-blog/{slug}", "/blog/{slug}", 301).  // Permanent
		WithRedirect("/temp-blog/{slug}", "/blog/{slug}", 302). // Temporary
		WithNotFoundPage(blog404Page).
		WithErrorPage(blog500Page).
		Incremental(10 * time.Minute).
		WithDependency("blog:posts").
		Build()

	globalNotFound := NewPage("global-404").
		WithContent(NewFragment("404", "errors/404.html").Build()).
		Build()

	globalError := NewPage("global-500").
		WithContent(NewFragment("500", "errors/500.html").Build()).
		Build()

	if blogPostPage.LayoutFragment != layout {
		t.Errorf("LayoutFragment = %v, want %v", blogPostPage.LayoutFragment, layout)
	}
	if blogPostPage.ContentFragment != blogPostContent {
		t.Errorf("ContentFragment = %v, want %v", blogPostPage.ContentFragment, blogPostContent)
	}
	wantLocales := []string{"en", "tr"}
	if locales := blogPostPage.Locales(); len(locales) != 2 || locales[0] != wantLocales[0] || locales[1] != wantLocales[1] {
		t.Errorf("Locales() = %v, want %v", locales, wantLocales)
	}
	if len(blogPostPage.Redirects) != 2 {
		t.Fatalf("len(Redirects) = %d, want 2", len(blogPostPage.Redirects))
	}
	if blogPostPage.Redirects[0].From != "/old-blog/{slug}" || blogPostPage.Redirects[0].To != "/blog/{slug}" || blogPostPage.Redirects[0].StatusCode != 301 {
		t.Errorf("Redirects[0] = %+v, want {From:/old-blog/{slug} To:/blog/{slug} StatusCode:301}", blogPostPage.Redirects[0])
	}
	if blogPostPage.Redirects[1].From != "/temp-blog/{slug}" || blogPostPage.Redirects[1].To != "/blog/{slug}" || blogPostPage.Redirects[1].StatusCode != 302 {
		t.Errorf("Redirects[1] = %+v, want {From:/temp-blog/{slug} To:/blog/{slug} StatusCode:302}", blogPostPage.Redirects[1])
	}
	if blogPostPage.NotFoundPage != blog404Page {
		t.Errorf("NotFoundPage = %v, want %v", blogPostPage.NotFoundPage, blog404Page)
	}
	if blogPostPage.ErrorPage != blog500Page {
		t.Errorf("ErrorPage = %v, want %v", blogPostPage.ErrorPage, blog500Page)
	}
	if blogPostPage.Strategy != StrategyIncremental {
		t.Errorf("Strategy = %v, want StrategyIncremental", blogPostPage.Strategy)
	}
	if blogPostPage.CacheTTL != 10*time.Minute {
		t.Errorf("CacheTTL = %v, want 10m", blogPostPage.CacheTTL)
	}
	if len(blogPostPage.DependencyTags) != 1 || blogPostPage.DependencyTags[0] != "blog:posts" {
		t.Errorf("DependencyTags = %v, want [blog:posts]", blogPostPage.DependencyTags)
	}

	if !blogPostContent.Required {
		t.Error("blogPostContent.Required = false, want true")
	}

	if blog404Page.LayoutFragment != layout || blog404Page.ContentFragment != blog404Content {
		t.Errorf("blog404Page = {Layout:%v Content:%v}, want {Layout:%v Content:%v}", blog404Page.LayoutFragment, blog404Page.ContentFragment, layout, blog404Content)
	}
	if blog500Page.LayoutFragment != layout || blog500Page.ContentFragment != blog500Content {
		t.Errorf("blog500Page = {Layout:%v Content:%v}, want {Layout:%v Content:%v}", blog500Page.LayoutFragment, blog500Page.ContentFragment, layout, blog500Content)
	}
	if globalNotFound.Name != "global-404" || globalNotFound.ContentFragment == nil {
		t.Errorf("globalNotFound = %+v, want Name=global-404 and a non-nil ContentFragment", globalNotFound)
	}
	if globalError.Name != "global-500" || globalError.ContentFragment == nil {
		t.Errorf("globalError = %+v, want Name=global-500 and a non-nil ContentFragment", globalError)
	}
}

// TestPageBuilder_WithSEO is not part of either spec example — WithSEO is exercised
// separately here since it carries the one permitted new `any` in this package.
func TestPageBuilder_WithSEO(t *testing.T) {
	content := NewFragment("c", "c.html").Build()

	page := NewPage("p").
		WithContent(content).
		WithSEO("title", "A blog post").
		WithSEO("views", 42).
		Build()

	if page.SEO["title"] != "A blog post" {
		t.Errorf(`SEO["title"] = %v, want "A blog post"`, page.SEO["title"])
	}
	if page.SEO["views"] != 42 {
		t.Errorf(`SEO["views"] = %v, want 42`, page.SEO["views"])
	}
}

func TestPageBuilder_StrategySelection(t *testing.T) {
	content := NewFragment("c", "c.html").Build()

	tests := []struct {
		name string
		set  func(*PageBuilder) *PageBuilder
		want RenderStrategy
	}{
		{"static", func(b *PageBuilder) *PageBuilder { return b.Static() }, StrategyStatic},
		{"dynamic", func(b *PageBuilder) *PageBuilder { return b.Dynamic() }, StrategyDynamic},
		{"incremental", func(b *PageBuilder) *PageBuilder { return b.Incremental(time.Minute) }, StrategyIncremental},
		{"unset defaults to dynamic", func(b *PageBuilder) *PageBuilder { return b }, StrategyDynamic},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.set(NewPage("p").WithContent(content))
			page := b.Build()
			if page.Strategy != tt.want {
				t.Errorf("Strategy = %v, want %v", page.Strategy, tt.want)
			}
		})
	}
}

func TestPageBuilder_Build_NeverNil(t *testing.T) {
	p := NewPage("").Build()
	if p == nil {
		t.Fatal("Build() = nil, want non-nil Page even for a builder with no calls")
	}
}

func TestPageBuilder_BuildErr_MissingContent(t *testing.T) {
	b := NewPage("no-content").WithPath("en", "/")
	p := b.Build()
	if p == nil {
		t.Fatal("Build() = nil, want non-nil Page")
	}
	if err := b.BuildErr(); !errors.Is(err, ErrMissingContent) {
		t.Fatalf("BuildErr() = %v, want error wrapping ErrMissingContent", err)
	}
}

func TestPageBuilder_BuildErr_NilWhenContentSet(t *testing.T) {
	content := NewFragment("c", "c.html").Build()
	b := NewPage("p").WithContent(content)
	b.Build()
	if err := b.BuildErr(); err != nil {
		t.Fatalf("BuildErr() = %v, want nil", err)
	}
}

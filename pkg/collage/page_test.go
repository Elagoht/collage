package collage

import (
	"errors"
	"testing"
	"time"
)

// TestPageBuilder_MinimalApp mirrors the framework's minimal-app usage: a single page
// with a content fragment and one path, using the default (dynamic) strategy.
func TestPageBuilder_MinimalApp(t *testing.T) {
	home := NewFragment("home", "pages/home.html").Build()

	pageBuilder := NewPage("home").
		WithContent(home).
		WithPath("en", "/")
	page := pageBuilder.Build()

	if err := pageBuilder.BuildErr(); err != nil {
		t.Fatalf("BuildErr() = %v, want nil", err)
	}
	if page.Name != "home" {
		t.Errorf("Name = %q, want %q", page.Name, "home")
	}
	if page.ContentFragment != home {
		t.Errorf("ContentFragment = %v, want %v", page.ContentFragment, home)
	}
	if got, ok := page.PathFor("en"); !ok || got != "/" {
		t.Errorf("PathFor(en) = (%q, %v), want (\"/\", true)", got, ok)
	}
	if page.Strategy != StrategyDynamic {
		t.Errorf("Strategy = %v, want StrategyDynamic (the default)", page.Strategy)
	}
}

// TestPageBuilder_BlogExample mirrors a blog-style page: a layout-wrapped post page,
// localized paths, a permanent and a temporary redirect, page-specific 404/500 pages,
// incremental caching with dependency tags, and SEO metadata.
func TestPageBuilder_BlogExample(t *testing.T) {
	layout := NewFragment("layout", "layout.html").
		WithSlot("content", true, false).
		Build()
	post := NewFragment("post", "pages/post.html").Required().Build()

	notFound := NewPage("blog-404").WithContent(NewFragment("404", "404.html").Build()).Build()
	serverError := NewPage("blog-500").WithContent(NewFragment("500", "500.html").Build()).Build()

	pageBuilder := NewPage("blog-post").
		WithLayout(layout).
		WithContent(post).
		WithPath("en", "/blog/{slug}").
		WithPath("tr", "/blog/{slug}").
		WithPermanentRedirect("/posts/{slug}", "/blog/{slug}").
		WithRedirect("/old-blog/{slug}", "/blog/{slug}", 302).
		WithNotFoundPage(notFound).
		WithErrorPage(serverError).
		Incremental(10*time.Minute).
		WithDependency("post:slug", "blog:posts").
		WithSEO("title", "A blog post").
		WithSEO("views", 42)
	page := pageBuilder.Build()

	if err := pageBuilder.BuildErr(); err != nil {
		t.Fatalf("BuildErr() = %v, want nil", err)
	}

	if page.LayoutFragment != layout {
		t.Errorf("LayoutFragment = %v, want %v", page.LayoutFragment, layout)
	}
	if page.ContentFragment != post {
		t.Errorf("ContentFragment = %v, want %v", page.ContentFragment, post)
	}
	wantLocales := []string{"en", "tr"}
	if locales := page.Locales(); len(locales) != 2 || locales[0] != wantLocales[0] || locales[1] != wantLocales[1] {
		t.Errorf("Locales() = %v, want %v", locales, wantLocales)
	}
	if len(page.Redirects) != 2 {
		t.Fatalf("len(Redirects) = %d, want 2", len(page.Redirects))
	}
	if !page.Redirects[0].Permanent || page.Redirects[0].From != "/posts/{slug}" {
		t.Errorf("Redirects[0] = %+v, want permanent redirect from /posts/{slug}", page.Redirects[0])
	}
	if page.Redirects[1].StatusCode != 302 || page.Redirects[1].From != "/old-blog/{slug}" {
		t.Errorf("Redirects[1] = %+v, want 302 redirect from /old-blog/{slug}", page.Redirects[1])
	}
	if page.NotFoundPage != notFound {
		t.Errorf("NotFoundPage = %v, want %v", page.NotFoundPage, notFound)
	}
	if page.ErrorPage != serverError {
		t.Errorf("ErrorPage = %v, want %v", page.ErrorPage, serverError)
	}
	if page.Strategy != StrategyIncremental {
		t.Errorf("Strategy = %v, want StrategyIncremental", page.Strategy)
	}
	if page.CacheTTL != 10*time.Minute {
		t.Errorf("CacheTTL = %v, want 10m", page.CacheTTL)
	}
	wantTags := []string{"post:slug", "blog:posts"}
	if len(page.DependencyTags) != 2 || page.DependencyTags[0] != wantTags[0] || page.DependencyTags[1] != wantTags[1] {
		t.Errorf("DependencyTags = %v, want %v", page.DependencyTags, wantTags)
	}
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

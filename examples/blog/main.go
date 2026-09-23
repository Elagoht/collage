// Command blog is collage-core's example application: the advanced example from
// the framework's specification, implemented for real and serving actual
// requests.
//
// It exercises, end to end, the parts of the framework that are easy to describe
// and hard to get right:
//
//   - one layout fragment shared by every page, including the error pages;
//   - a home page listing an in-memory post store;
//   - "/blog/{slug}" with a Required content fragment, incremental caching, and
//     the dependency tags "post:<slug>" and "blog:posts";
//   - a page-specific 404 and a page-specific 500, reached by the two different
//     ways a data handler can fail, plus site-wide defaults for every other page;
//   - a permanent (301) and a temporary (302) redirect from older URL shapes;
//   - a plugin that post-processes every rendered page and contributes a CLI
//     subcommand;
//   - "/sitemap.xml" and "/robots.txt" as documents: routed, cached, non-HTML
//     responses whose handlers return bytes rather than rendering templates;
//   - "/static/" as a mounted asset file system, served from an embed.FS with
//     Range support and its own Cache-Control, outside the page cache entirely.
//
// It imports nothing but the standard library and
// github.com/Elagoht/collage/pkg/collage. If this program ever needed an
// internal/ import, that would be a hole in the framework's public surface
// rather than something to work around here.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// templateRoot is the directory the example's templates are loaded from,
// relative to this directory — which is both where "go run ." starts and where
// "go test" runs, so the same value serves the server and the test.
const templateRoot = "templates"

// site is the metadata the shared layout renders with. A real application would
// read it from configuration; here it is a constant so the layout's data handler
// stays about the mechanism rather than about the source.
var site = siteInfo{
	Name:    "The collage blog",
	Tagline: "Server-side components, composed from fragments.",
}

// siteInfo is the site-wide metadata the layout template renders.
type siteInfo struct {
	// Name is the site's title, rendered in <title> and in the header.
	Name string
	// Tagline is the one-line description rendered in the footer.
	Tagline string
}

// pageData is everything this blog's templates render with. One type covers every
// fragment: the layout reads Site, the home page reads Posts, and the post page
// reads Post, and each handler fills in only the fields its own template touches.
//
// Having a single concrete type is what keeps this program down to one `any` in
// total, in bind below, instead of one per data handler.
type pageData struct {
	// Site is the site-wide metadata the layout renders.
	Site siteInfo
	// Posts is the post index the home page lists. It is nil on every other page.
	Posts []Post
	// Post is the single post the post page renders. It is nil on every other
	// page.
	Post *Post
}

// dataFunc is a data handler written in terms of this program's own concrete data
// type. Every handler below is one of these; bind turns it into the
// collage.DataHandlerFunc the framework wants.
type dataFunc func(ctx context.Context, rc *collage.RenderContext) (*pageData, []string, error)

// bind adapts fn to collage.DataHandlerFunc, whose data return is declared `any`
// because html/template renders arbitrary data and the framework has no way to
// know a given application's shape.
//
// This is the single place in the example where that `any` is spelled out. Every
// handler is written against *pageData and the compiler checks it as such.
func bind(fn dataFunc) collage.DataHandlerFunc {
	return func(ctx context.Context, rc *collage.RenderContext) (any, []string, error) { // any: restates collage.DataHandlerFunc's own already-justified declaration
		data, tags, err := fn(ctx, rc)
		if err != nil {
			// Returning data here would box a nil *pageData into a non-nil
			// interface value. Nothing reads it on the error path today, but a
			// typed nil masquerading as a value is not worth leaving lying
			// around.
			return nil, tags, err
		}
		return data, tags, nil
	}
}

func main() {
	host := flag.String("host", "localhost", "interface to listen on")
	port := flag.Int("port", 3000, "port to listen on; pick another if 3000 is taken")
	flag.Parse()

	app, _, err := newBlog(templateRoot, *host, *port)
	if err != nil {
		log.Fatalf("blog: %v", err)
	}

	// No "starting" line here: ListenAndServe logs "collage: listening" once the
	// port is actually bound, which is the only moment the claim is true. Printing
	// one before the call is how a bind failure gets read as a working server.
	if err := app.ListenAndServe(); err != nil {
		log.Fatalf("blog: %v", err)
	}
}

// newBlog builds the whole application over templateRoot and returns it
// alongside the store it serves from, ready for ListenAndServe or for an
// httptest server.
//
// Nothing here is deferred to the first request: an unparseable template, a
// fragment naming a template that does not exist, a page referencing an error
// page that was never registered — every one of those is reported from this
// function, at startup, by design.
func newBlog(root, host string, port int) (*collage.App, *PostStore, error) {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{
			Host: host,
			Port: port,
		},
		Template: collage.TemplateConfig{
			Root:      root,
			Extension: ".html",
		},
		Cache: collage.CacheConfig{
			Enabled:    true,
			Type:       "memory",
			DefaultTTL: 5 * time.Minute,
		},
		Locale: collage.LocaleConfig{
			Default:   "en",
			Supported: []string{"en", "tr"},
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build app: %w", err)
	}

	store := NewPostStore()

	// One layout fragment, shared by all five pages below. Registration gives
	// each page its own copy of the layout's slot table and binds that page's
	// content into it, so sharing a layout is safe: the pages do not end up
	// rendering each other's content out of one shared slot.
	layout := collage.NewFragment("layout", "layouts/default.html").
		WithSlot("content", true, false).
		WithDataHandler(bind(siteData)).
		Build()

	homeContent := collage.NewFragment("home-content", "pages/home.html").
		WithDataHandler(bind(func(_ context.Context, _ *collage.RenderContext) (*pageData, []string, error) {
			// "blog:posts" is this render's dependency: invalidating that tag
			// drops the cached index, which is what a newly published post
			// should do.
			return &pageData{Site: site, Posts: store.List()}, []string{"blog:posts"}, nil
		})).
		Build()

	postContent := collage.NewFragment("blog-post", "pages/blog-post.html").
		WithDataHandler(bind(func(ctx context.Context, rc *collage.RenderContext) (*pageData, []string, error) {
			slug := rc.Param("slug")
			post, err := store.Post(ctx, slug)
			if err != nil {
				// The error is passed through unchanged, and that is
				// load-bearing: the store wrapped either collage.ErrNotFound or
				// ErrStorageUnavailable, and the framework tells the two apart
				// with errors.Is to choose between this page's 404 and its 500.
				return nil, nil, err
			}
			return &pageData{Site: site, Post: &post}, []string{"post:" + slug}, nil
		})).
		// Required: a post page without its post is not a page worth serving. An
		// optional fragment would have rendered the layout around a hole and
		// answered 200.
		Required().
		WithTimeout(2 * time.Second).
		Build()

	// The two pages this blog serves in place of the site-wide error pages. They
	// have no paths of their own: they are reached by failing, not by matching.
	blog404Page := collage.NewPage("blog-404").
		WithLayout(layout).
		WithContent(collage.NewFragment("blog-404-content", "errors/blog-404.html").Build()).
		Build()

	blog500Page := collage.NewPage("blog-500").
		WithLayout(layout).
		WithContent(collage.NewFragment("blog-500-content", "errors/blog-500.html").Build()).
		Build()

	postPage := collage.NewPage("blog-post").
		WithLayout(layout).
		WithContent(postContent).
		WithPath("en", "/blog/{slug}").
		WithPath("tr", "/blog/{slug}").
		WithRedirect("/old-blog/{slug}", "/blog/{slug}", 301).  // Permanent.
		WithRedirect("/temp-blog/{slug}", "/blog/{slug}", 302). // Temporary.
		WithNotFoundPage(blog404Page).
		WithErrorPage(blog500Page).
		Incremental(10 * time.Minute).
		WithDependency("blog:posts").
		Build()

	homePage := collage.NewPage("home").
		WithLayout(layout).
		WithContent(homeContent).
		WithPath("en", "/").
		WithPath("tr", "/").
		Incremental(time.Minute).
		WithDependency("blog:posts").
		Build()

	// The site-wide fallbacks, for every page that does not name its own. They
	// deliberately have no layout: an error page that depends on the site chrome
	// is an error page that can fail for the same reason the request did.
	globalNotFound := collage.NewPage("global-404").
		WithContent(collage.NewFragment("404", "errors/404.html").Build()).
		Build()

	globalError := collage.NewPage("global-500").
		WithContent(collage.NewFragment("500", "errors/500.html").Build()).
		Build()

	// The builders accumulate their errors rather than returning them (see
	// collage.FragmentBuilder), and nothing above checks BuildErr. Nothing is
	// lost by that: registration validates every page and refuses a malformed
	// one by name, which is what the error checks below surface.
	//
	// blog404Page and blog500Page must be registered in their own right. A
	// referenced-but-unregistered error page never has its content bound into
	// its layout, so it would render empty at the one moment it was needed —
	// which is why startup rejects it outright.
	for _, page := range []*collage.Page{homePage, postPage, blog404Page, blog500Page} {
		if err := app.RegisterPage(page); err != nil {
			return nil, nil, fmt.Errorf("register page: %w", err)
		}
	}
	if err := app.RegisterNotFoundPage(globalNotFound); err != nil {
		return nil, nil, fmt.Errorf("register global 404: %w", err)
	}
	if err := app.RegisterErrorPage(globalError); err != nil {
		return nil, nil, fmt.Errorf("register global 500: %w", err)
	}
	if err := app.RegisterPlugin(&stamp{store: store}); err != nil {
		return nil, nil, fmt.Errorf("register plugin: %w", err)
	}

	// The two non-HTML routes. They register into the same router the pages
	// above did, so "/sitemap.xml" colliding with a page path would be a startup
	// error here rather than a coin toss at request time.
	for _, doc := range []*collage.Document{newSitemapDocument(store), newRobotsDocument(), newFeedDocument(store)} {
		if err := app.RegisterDocument(doc); err != nil {
			return nil, nil, fmt.Errorf("register document: %w", err)
		}
	}

	// The stylesheet the layout links. A mount claims its whole URL prefix, so a
	// prefix that would shadow a registered page or document is refused when the
	// handler is built — whichever of the two was registered first.
	if err := mountAssets(app); err != nil {
		return nil, nil, fmt.Errorf("mount assets: %w", err)
	}

	return app, store, nil
}

// siteData is the layout's data handler. It returns no dependency tags: the site
// metadata is a constant, so there is nothing whose change should invalidate a
// cached page.
func siteData(_ context.Context, _ *collage.RenderContext) (*pageData, []string, error) {
	return &pageData{Site: site}, nil, nil
}

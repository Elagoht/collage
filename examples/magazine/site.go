package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Elagoht/collage/pkg/collage"

	jsonld "github.com/Elagoht/collage-jsonld"
	minimizer "github.com/Elagoht/collage-minimizer"
	optimage "github.com/Elagoht/collage-opti-image"
)

// templatesFS carries the site's markup. Embedding it is what lets the binary run
// from any working directory, which is what makes a single-binary deployment
// possible: nothing on disk beside it is required.
//
//go:embed templates
var templatesFS embed.FS

// templateRoot is the directory within templatesFS. embed.FS names files by their
// path in the source tree, so stripping this prefix is what makes the layout
// "layouts/magazine.html" rather than "templates/layouts/magazine.html".
const templateRoot = "templates"

// config is everything the site reads from its environment.
type config struct {
	// Host and Port are the address to serve on.
	Host string
	Port int
	// APIBaseURL is where the newsroom API lives.
	APIBaseURL string
	// PublicBaseURL is the site's own origin, used to build absolute URLs in the
	// feed and the sitemap. A feed with relative links is a feed no reader can
	// follow, and the site cannot infer its public origin from the address it binds
	// — behind a proxy those are different strings.
	PublicBaseURL string
	// CacheTTL is the default page cache lifetime.
	CacheTTL time.Duration
	// DevMode surfaces failed fragments as HTML comments and disables template
	// caching.
	DevMode bool
	// PluginConfig is each plugin's own configuration, keyed by plugin name.
	PluginConfig map[string]json.RawMessage
	// CacheDir keeps rendered pages on disk so a restart serves them rather than
	// rendering them again. Empty means memory only.
	CacheDir string
	// Logger receives the framework's own structured output as well as the site's.
	Logger *slog.Logger
}

// newSite builds the whole application and returns it alongside the API client it
// reads through.
//
// Nothing is deferred to the first request. An unparseable template, a fragment
// naming a template that does not exist, a page whose error page was never
// registered, a mount prefix that shadows a route — every one of those is reported
// from this function, at startup.
func newSite(cfg config) (*collage.App, *Client, error) {
	app, err := collage.New(&collage.Config{
		DevMode: cfg.DevMode,
		Logger:  cfg.Logger,
		// Three ordinary dependencies, fetched with go get like any other. Two of
		// them need the Configure phase — the minifier wraps every mounted
		// filesystem, the image optimiser registers the route it serves from —
		// and both happen while the application is built, so they have to arrive
		// here rather than through RegisterPlugin.
		Plugins: []collage.Plugin{
			minimizer.New(),
			jsonld.New(),
			optimage.New(),
		},
		PluginConfig: cfg.PluginConfig,
		Server: collage.ServerConfig{
			Host: cfg.Host,
			Port: cfg.Port,
		},
		Template: collage.TemplateConfig{
			FS:        templatesFS,
			Root:      templateRoot,
			Extension: ".html",
			// Bounds every data handler that sets no timeout of its own, and is
			// the only bound on a document handler. It must exceed the API
			// client's worst case — attempts × (timeout + retry delay) — or the
			// render gives up before the retry that would have succeeded.
			Timeout: 6 * time.Second,
		},
		Cache: collage.CacheConfig{
			Enabled: true,
			// Disk when a directory is configured, so a restart serves what the
			// last run rendered. The framework substitutes memory in dev mode
			// whatever this says, because that is where the output changes between
			// runs and no version bump would catch it.
			Type: cacheType(cfg.CacheDir),
			Dir:  cfg.CacheDir,
			// No Version: the framework derives one from the running executable,
			// which changes exactly when this site's output might.
			DefaultTTL:    cfg.CacheTTL,
			MaxEntries:    512,
			MaxKeysPerTag: 2048,
		},
		Locale: collage.LocaleConfig{
			Default:   defaultLocale,
			Supported: supportedLocales,
			// Locale comes from the URL and nowhere else.
			//
			// This site registers different paths per locale, so a locale
			// resolved from a header or a cookie can disagree with the path the
			// reader actually requested: an English link shared with someone
			// whose browser asks for Turkish would resolve to "tr", look
			// "/category/climate" up in the Turkish tree, and 404 on a URL that
			// works perfectly for the person who sent it.
			//
			// With both disabled, a URL means the same page for everyone, which
			// is also what makes the pages shareable and the cache sound.
			DisableHeaderLocale: true,
			DisableCookieLocale: true,
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build app: %w", err)
	}

	client := NewClient(cfg.APIBaseURL)
	d := &deps{
		client:      client,
		log:         cfg.Logger,
		apiBase:     cfg.APIBaseURL,
		subscribers: newSubscribers(),
		pages:       &sitePages{},
	}

	// One layout, shared by every page.
	//
	// It used to be built per page, because the document head was a slot and only a
	// per-page layout could carry a per-page head fragment. Hoisting removed the
	// reason: the layout writes a default title and a {{hoist "head"}}, and a page's
	// content overrides the title from wherever it is nested.
	//
	// Its own data handler performs no I/O, and that is the point: the layout wraps
	// every page including the error pages, so a layout that can fail is a layout
	// that can take down the page explaining why the site is broken. Everything the
	// chrome needs from the backend hangs off a slot with a fallback.
	layout := collage.NewFragment("layout", "layouts/magazine.html").
		WithSlot("content", true, false).
		WithSlot("nav", false, false).
		WithSlot("sidebar", false, false).
		WithSlotFragment("nav", collage.NewFragment("nav", "partials/nav.html").
			WithDataHandler(bind(d.navData)).
			WithFallback(collage.NewFragment("nav-unavailable", "partials/nav-unavailable.html").
				WithDataHandler(bind(d.chromeFallbackData)).
				Build()).
			WithTimeout(2*time.Second).
			Build()).
		WithSlotFragment("sidebar", collage.NewFragment("popular", "partials/popular.html").
			WithDataHandler(bind(d.popularData)).
			WithFallback(collage.NewFragment("popular-unavailable", "partials/popular-unavailable.html").
				WithDataHandler(bind(d.chromeFallbackData)).
				Build()).
			WithTimeout(2*time.Second).
			Build()).
		WithDataHandler(bind(d.layoutData)).
		Build()

	// The error pages. They use the same layout, which is safe precisely because the
	// layout does no I/O: the condition that broke the page cannot also break the
	// page that reports it.
	notFound := collage.NewPage("not-found").
		WithLayout(layout).
		WithContent(collage.NewFragment("404", "errors/404.html").
			WithDataHandler(bind(d.notFoundData)).
			Build()).
		Build()

	articleNotFound := collage.NewPage("article-not-found").
		WithLayout(layout).
		WithContent(collage.NewFragment("article-404", "errors/article-404.html").
			WithDataHandler(bind(d.notFoundData)).
			Build()).
		Build()

	serverError := collage.NewPage("server-error").
		WithLayout(layout).
		WithContent(collage.NewFragment("500", "errors/500.html").
			WithDataHandler(bind(d.serverErrorData)).
			Build()).
		Build()

	home := collage.NewPage("home").
		WithLayout(layout).
		WithContent(collage.NewFragment("home-content", "pages/home.html").
			WithDataHandler(bind(d.homeData)).
			Required().
			Build()).
		WithPath("en", "/").
		WithPath("tr", "/").
		// Only the page number changes what this renders. Without saying so, every
		// "?utm_source=..." a newsletter or a crawler appends would mint its own
		// cache entry and evict a real page to make room.
		WithCacheParams("page").
		WithNotFoundPage(notFound).
		WithErrorPage(serverError).
		Incremental(time.Minute).
		WithDependency("articles").
		Build()

	// The locale-specific paths. This is the reason DisableHeaderLocale is set
	// above: "/category/climate" and "/kategori/climate" are different routes in
	// different trees, and the URL is what decides which one a request means.
	category := collage.NewPage("category").
		WithLayout(layout).
		WithContent(collage.NewFragment("category-content", "pages/category.html").
			WithDataHandler(bind(d.categoryData)).
			Required().
			Build()).
		WithPath("en", "/category/{slug}").
		WithPath("tr", "/kategori/{slug}").
		WithCacheParams("page").
		WithNotFoundPage(notFound).
		WithErrorPage(serverError).
		Incremental(2 * time.Minute).
		WithDependency("articles").
		Build()

	author := collage.NewPage("author").
		WithLayout(layout).
		WithContent(collage.NewFragment("author-content", "pages/author.html").
			WithDataHandler(bind(d.authorData)).
			Required().
			Build()).
		WithPath("en", "/author/{slug}").
		WithPath("tr", "/yazar/{slug}").
		WithCacheParams("page").
		WithNotFoundPage(notFound).
		WithErrorPage(serverError).
		Incremental(5 * time.Minute).
		WithDependency("articles").
		Build()

	article := collage.NewPage("article").
		WithLayout(layout).
		WithContent(collage.NewFragment("article-content", "pages/article.html").
			WithDataHandler(bind(d.articleData)).
			// Required: a piece without its text is not a page worth serving. An
			// optional fragment would render the chrome around a hole and answer
			// 200, which is how a broken article gets indexed as an empty one.
			Required().
			WithTimeout(3*time.Second).
			Build()).
		WithPath("en", "/{year}/{month}/{slug}").
		WithPath("tr", "/{year}/{month}/{slug}").
		// No arguments: an article renders the same whatever the query says, so
		// nothing in it should reach the cache key.
		WithCacheParams().
		WithNotFoundPage(articleNotFound).
		WithErrorPage(serverError).
		Incremental(15 * time.Minute).
		WithDependency("articles").
		Build()

	// Search is the one page that is never cached.
	//
	// WithCacheParams does not rescue it. That bounds *which* parameters
	// discriminate, and here the offending parameter is the one the page is about:
	// "q" has to discriminate, and its values are chosen by whoever is asking. A
	// cache keyed on arbitrary reader input is an eviction attack with a text
	// field for a trigger. Rendering fresh costs one API call.
	// The results are a fragment of their own so that they can be fetched on their
	// own. Inside the page it renders where the template puts it; at
	// /search/results it is the whole response, which is what lets a search box
	// refresh its list without reloading the header, the navigation and the
	// sidebar around it.
	searchResults := collage.NewFragment("search-results", "partials/results.html").
		WithDataHandler(bind(d.searchData)).
		Required().
		Build()

	search := collage.NewPage("search").
		WithLayout(layout).
		WithContent(collage.NewFragment("search-content", "pages/search.html").
			WithDataHandler(bind(d.searchData)).
			WithSlot("results", false, false).
			WithSlotFragment("results", searchResults).
			Required().
			Build()).
		WithPath("en", "/search").
		WithPath("tr", "/arama").
		WithFragmentPath("en", "/search/results", searchResults).
		WithFragmentPath("tr", "/arama/sonuclar", searchResults).
		WithNotFoundPage(notFound).
		WithErrorPage(serverError).
		Dynamic().
		Build()

	// articleNotFound and the other error pages must be registered in their own
	// right: a referenced-but-unregistered error page never has its content bound
	// into its layout, so it would render empty at the one moment it is needed.
	// The newsletter has a page of its own rather than sitting on the home page,
	// and the reason is worth stating: a form needs a server to post to, so a page
	// carrying one cannot be part of a static build. The home page is built —
	// it is the site's front door — so the form lives somewhere that is not.
	//
	// It is also the right shape for a form that appears site-wide. A form in a
	// footer is not about the page it happens to be on, and answering a failed
	// submission by re-rendering "whichever page they were reading" is a question
	// with no good answer.
	newsletter := collage.NewPage("newsletter").
		WithLayout(layout).
		WithContent(collage.NewFragment("newsletter-content", "pages/newsletter.html").
			WithDataHandler(bind(d.newsletterData)).
			Required().
			Build()).
		WithPath("en", "/newsletter").
		WithPath("tr", "/bulten").
		WithNotFoundPage(notFound).
		WithErrorPage(serverError).
		Dynamic().
		WithAction("POST", d.subscribe).
		Build()

	// The action re-renders this page on a validation failure, and needs the
	// finished page — registration is what binds a page's content into its
	// layout, so a page built on the spot would render an empty layout.
	d.pages.newsletter = newsletter

	for _, page := range []*collage.Page{home, category, author, article, search, newsletter, notFound, articleNotFound, serverError} {
		if err := app.RegisterPage(page); err != nil {
			return nil, nil, fmt.Errorf("register page %q: %w", page.Name, err)
		}
	}
	if err := app.RegisterNotFoundPage(notFound); err != nil {
		return nil, nil, fmt.Errorf("register site 404: %w", err)
	}
	if err := app.RegisterErrorPage(serverError); err != nil {
		return nil, nil, fmt.Errorf("register site 500: %w", err)
	}

	// Documents register into the same router the pages did, so a document path
	// colliding with a page path is a startup error rather than a coin toss at
	// request time.
	for _, doc := range []*collage.Document{
		d.newFeedDocument(cfg.PublicBaseURL),
		d.newSitemapDocument(cfg.PublicBaseURL),
		d.newRobotsDocument(cfg.PublicBaseURL),
		d.newHealthDocument(),
	} {
		if err := app.RegisterDocument(doc); err != nil {
			return nil, nil, fmt.Errorf("register document %q: %w", doc.Name, err)
		}
	}

	if err := mountStatic(app); err != nil {
		return nil, nil, fmt.Errorf("mount static: %w", err)
	}

	return app, client, nil
}

// cacheType picks the cache implementation from whether a directory was configured.
func cacheType(dir string) string {
	if dir == "" {
		return "memory"
	}
	return "disk"
}

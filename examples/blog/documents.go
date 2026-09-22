package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// siteBaseURL is the absolute origin the sitemap's <loc> entries are built from.
// A sitemap's URLs must be absolute, and the server has no reliable way to know
// its own public origin — a reverse proxy can terminate TLS and rewrite the Host
// — so a real application configures this rather than deriving it from the
// request.
const siteBaseURL = "https://blog.example.com"

// sitemapContentType is the Content-Type the sitemap document is served with. A
// document's content type is static and required: the framework writes it
// verbatim on every response, including the ones served from cache, and never
// sniffs or guesses it.
const sitemapContentType = "application/xml"

// robotsContentType is the Content-Type the robots.txt document is served with.
const robotsContentType = "text/plain; charset=utf-8"

// robotsBody is the exact body /robots.txt serves. It is a constant because
// nothing about it depends on application state — which is also why the document
// below is Static() rather than Incremental.
const robotsBody = `User-agent: *
Allow: /
Disallow: /old-blog/
Disallow: /temp-blog/

Sitemap: ` + siteBaseURL + `/sitemap.xml
`

// sitemapURLSet is the root element of a sitemaps.org urlset document.
type sitemapURLSet struct {
	// XMLName fixes the element name encoding/xml marshals this struct as.
	XMLName xml.Name `xml:"urlset"`
	// Namespace is the sitemaps.org XML namespace, required by the schema.
	Namespace string `xml:"xmlns,attr"`
	// URLs is one entry per page a crawler should know about.
	URLs []sitemapURL `xml:"url"`
}

// sitemapURL is one <url> entry in a sitemap.
type sitemapURL struct {
	// Location is the page's absolute URL.
	Location string `xml:"loc"`
	// LastModified is the date the page last changed, in the W3C date format
	// the sitemaps.org schema requires.
	LastModified string `xml:"lastmod,omitempty"`
}

// newSitemapDocument returns the document served at "/sitemap.xml": the home
// page plus one entry per post, regenerated whenever "blog:posts" is
// invalidated.
//
// Two things about it are worth copying.
//
// The body is produced by encoding/xml, not by a template. A document renders no
// templates at all — its handler returns bytes — and that is deliberate rather
// than a missing feature: html/template applies HTML escaping rules, which are
// wrong for XML, so a sitemap built through it is silently malformed at exactly
// the characters that matter. A handler that wants templating reaches for
// text/template, or, as here, for the encoder that knows the format.
//
// Incremental(time.Hour) plus WithDependency("blog:posts") is the same caching
// contract a page gets, through the same machinery: the same cache key, the same
// content-hash ETag, the same conditional responses, and the same tag
// invalidation. Publishing a post calls InvalidateTags(ctx, "blog:posts") once
// and both the home page and this sitemap regenerate.
func newSitemapDocument(store *PostStore) *collage.Document {
	return collage.NewDocument("sitemap", sitemapContentType).
		WithPath("en", "/sitemap.xml").
		WithHandler(func(_ context.Context, _ *collage.RenderContext) ([]byte, []string, error) {
			return buildSitemap(store.Sitemap())
		}).
		Incremental(time.Hour).
		WithDependency("blog:posts").
		Build()
}

// buildSitemap marshals posts into a sitemaps.org document, and returns it
// alongside the dependency tags it was derived from.
//
// The tags are the handler's half of the caching contract: the document's own
// WithDependency("blog:posts") covers the index as a whole, and the per-post tags
// returned here mean that invalidating one post's tag also drops the sitemap that
// listed it. The two sources are unioned, de-duplicated and sorted by the
// framework.
func buildSitemap(posts []Post) ([]byte, []string, error) {
	set := sitemapURLSet{
		Namespace: "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs:      make([]sitemapURL, 0, len(posts)+1),
	}
	set.URLs = append(set.URLs, sitemapURL{Location: siteBaseURL + "/"})

	tags := make([]string, 0, len(posts)+1)
	tags = append(tags, "blog:posts")
	for _, post := range posts {
		set.URLs = append(set.URLs, sitemapURL{
			Location:     siteBaseURL + "/blog/" + post.Slug,
			LastModified: post.Published.Format(time.DateOnly),
		})
		tags = append(tags, "post:"+post.Slug)
	}

	encoded, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		// Nothing here can fail today — the struct has no unmarshalable field —
		// but returning the error rather than discarding it is what makes a
		// later field that can fail a 500 with a cause in the log, instead of a
		// silently truncated sitemap.
		return nil, tags, fmt.Errorf("blog: marshal sitemap: %w", err)
	}

	body := make([]byte, 0, len(xml.Header)+len(encoded)+1)
	body = append(body, xml.Header...)
	body = append(body, encoded...)
	body = append(body, '\n')
	return body, tags, nil
}

// newRobotsDocument returns the document served at "/robots.txt".
//
// It is Static(): its body never changes, so it is rendered once and served from
// cache until something explicitly invalidates it — which nothing here ever
// does. It declares no dependency tags for the same reason.
//
// The handler returns a non-empty body, and that is not incidental. A document
// handler that returns an empty body with a nil error is treated as a failure,
// not as a valid empty response: unlike a page, a document has no optional root
// fragment whose empty render is a deliberate outcome, so an empty success
// cannot be told apart from a handler that forgot to fill its body. A handler
// that genuinely wants to serve an empty document returns a single newline.
func newRobotsDocument() *collage.Document {
	return collage.NewDocument("robots", robotsContentType).
		WithPath("en", "/robots.txt").
		WithHandler(func(_ context.Context, _ *collage.RenderContext) ([]byte, []string, error) {
			return []byte(robotsBody), nil, nil
		}).
		Static().
		Build()
}

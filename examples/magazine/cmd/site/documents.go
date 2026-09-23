package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"time"

	"github.com/Elagoht/collage/examples/magazine/newsroom"
	"github.com/Elagoht/collage/pkg/collage"
)

// feedLimit is how many articles the feed carries. A feed is a recency window, not
// an archive; the sitemap is what a crawler walks for everything.
const feedLimit = newsroom.MaxPerPage

// rss, channel and item are the feed's wire shape.
//
// They are marshalled with encoding/xml rather than assembled with string
// concatenation or rendered through a template. html/template applies HTML escaping
// rules, which are wrong for XML at exactly the characters that most need escaping:
// an apostrophe in a headline becomes &#39; in a feed reader's title bar, and a
// naked ampersand makes the document unparseable.
type rss struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel channel  `xml:"channel"`
}

type channel struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	Language    string `xml:"language"`
	Items       []item `xml:"item"`
}

type item struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
	Author      string `xml:"author"`
	Category    string `xml:"category"`
	PubDate     string `xml:"pubDate"`
}

// urlset and sitemapURL are the sitemap's wire shape, for the same reason.
type urlset struct {
	XMLName xml.Name     `xml:"urlset"`
	NS      string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc        string `xml:"loc"`
	LastMod    string `xml:"lastmod,omitempty"`
	ChangeFreq string `xml:"changefreq,omitempty"`
}

// newFeedDocument builds "/rss.xml".
//
// It is a document, not a page: the handler returns bytes and the framework never
// runs them through the template engine. It shares the page cache and the dependency
// tracker, so "articles" invalidates the feed alongside every listing that declares
// the same tag.
func (d *deps) newFeedDocument(baseURL string) *collage.Document {
	return collage.NewDocument("feed", "application/rss+xml; charset=utf-8").
		WithPath("en", "/rss.xml").
		WithPath("tr", "/rss.xml").
		WithHandler(func(ctx context.Context, _ *collage.RenderContext) ([]byte, []string, error) {
			listing, err := d.client.Articles(ctx, newsroom.Filter{PerPage: feedLimit})
			if err != nil {
				return nil, nil, err
			}

			feed := rss{
				Version: "2.0",
				Channel: channel{
					Title:       site.Name,
					Link:        baseURL + "/",
					Description: site.Tagline,
					Language:    defaultLocale,
				},
			}
			for _, art := range listing.Items {
				feed.Channel.Items = append(feed.Channel.Items, item{
					Title:       art.Title,
					Link:        baseURL + art.Path(),
					GUID:        baseURL + art.Path(),
					Description: art.Dek,
					Author:      art.Author,
					Category:    art.Category,
					PubDate:     art.PublishedAt.Format(time.RFC1123Z),
				})
			}

			return marshalXML(feed)
		}).
		Incremental(15 * time.Minute).
		WithDependency("articles").
		Build()
}

// newSitemapDocument builds "/sitemap.xml": every article, every category landing
// page and every author landing page, in both locales.
//
// Both locales are listed because the site registers different paths for each, so
// the Turkish pages are URLs a crawler has no other way to discover — nothing links
// to them except the language toggle on the front page.
func (d *deps) newSitemapDocument(baseURL string) *collage.Document {
	return collage.NewDocument("sitemap", "application/xml; charset=utf-8").
		WithPath("en", "/sitemap.xml").
		WithPath("tr", "/sitemap.xml").
		WithHandler(func(ctx context.Context, _ *collage.RenderContext) ([]byte, []string, error) {
			listing, err := d.client.Articles(ctx, newsroom.Filter{PerPage: newsroom.MaxPerPage})
			if err != nil {
				return nil, nil, err
			}
			cats, err := d.client.Categories(ctx)
			if err != nil {
				return nil, nil, err
			}

			set := urlset{NS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
			for _, locale := range supportedLocales {
				u := urls{Locale: locale}
				set.URLs = append(set.URLs, sitemapURL{Loc: baseURL + u.Home(), ChangeFreq: "hourly"})
				for _, cat := range cats {
					set.URLs = append(set.URLs, sitemapURL{Loc: baseURL + u.Category(cat.Slug), ChangeFreq: "daily"})
				}
				for _, art := range listing.Items {
					set.URLs = append(set.URLs, sitemapURL{
						Loc:        baseURL + u.Article(art),
						LastMod:    art.PublishedAt.Format("2006-01-02"),
						ChangeFreq: "monthly",
					})
				}
			}

			return marshalXML(set)
		}).
		Incremental(time.Hour).
		WithDependency("articles", "categories").
		Build()
}

// newRobotsDocument builds "/robots.txt".
//
// The search page is disallowed on purpose. Its cache key includes the raw query
// string, so a crawler walking "?q=" permutations would mint a distinct cached
// representation per permutation — which is why the page is Dynamic here, and why
// pointing crawlers away from it is the other half of the same decision.
func (d *deps) newRobotsDocument(baseURL string) *collage.Document {
	body := fmt.Sprintf("User-agent: *\nAllow: /\nDisallow: /search\nDisallow: /tr/arama\n\nSitemap: %s/sitemap.xml\n", baseURL)
	payload := []byte(body)

	return collage.NewDocument("robots", "text/plain; charset=utf-8").
		WithPath("en", "/robots.txt").
		WithPath("tr", "/robots.txt").
		WithHandler(func(context.Context, *collage.RenderContext) ([]byte, []string, error) {
			return payload, nil, nil
		}).
		Static().
		Build()
}

// newHealthDocument builds "/healthz".
//
// It is a Document because that is what this framework calls a routed response that
// is not a rendered page: the handler returns bytes and a content type, and Dynamic
// keeps it out of the cache, which is the whole requirement for a health endpoint.
//
// It answers 200 with "degraded" rather than 503 when the API is down. That is a
// deliberate distinction between liveness and readiness: the site is still serving —
// cached pages, error pages, static files — and an orchestrator that restarts it for
// an upstream outage is restarting the wrong process. The body says which it is, so
// a readiness probe that does care can read it.
func (d *deps) newHealthDocument() *collage.Document {
	return collage.NewDocument("health", "application/json; charset=utf-8").
		WithPath("en", "/healthz").
		WithPath("tr", "/healthz").
		WithHandler(func(ctx context.Context, _ *collage.RenderContext) ([]byte, []string, error) {
			status := map[string]string{"status": "ok", "newsroom": "reachable"}
			if err := d.client.Health(ctx); err != nil {
				d.log.Warn("site: newsroom API unreachable", "err", err)
				status["status"] = "degraded"
				status["newsroom"] = "unreachable"
			}
			body, err := json.Marshal(status)
			if err != nil {
				return nil, nil, fmt.Errorf("marshal health: %w", err)
			}
			return append(body, '\n'), nil, nil
		}).
		Dynamic().
		Build()
}

// marshalXML serialises v with the XML declaration prepended. encoding/xml does not
// emit one, and a feed without it is technically parseable and practically a source
// of encoding bugs in readers that guess.
func marshalXML(v any) ([]byte, []string, error) { // any: encoding/xml's own parameter type
	body, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal xml: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.Write(body)
	buf.WriteByte('\n')
	return buf.Bytes(), []string{"articles"}, nil
}

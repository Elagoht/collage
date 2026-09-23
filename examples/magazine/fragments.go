package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"

	"github.com/Elagoht/collage/pkg/collage"
)

// newsroomData is the signature every data handler in this file has before bind
// adapts it to the framework's. Writing them typed and adapting once is what keeps
// "any" out of the handlers themselves.
type newsroomData func(context.Context, *collage.RenderContext) (*view, []string, error)

// bind adapts a typed handler to collage.DataHandlerFunc.
func bind(fn newsroomData) collage.DataHandlerFunc {
	return func(ctx context.Context, rc *collage.RenderContext) (any, []string, error) { // any: restates collage.DataHandlerFunc's own already-justified declaration
		data, tags, err := fn(ctx, rc)
		if err != nil {
			// Returning data here would box a nil *view into a non-nil interface
			// value: a typed nil that reads as present.
			return nil, tags, err
		}
		return data, tags, nil
	}
}

// deps is what the data handlers read. One struct rather than a closure per handler
// so the set of things a handler can reach is visible in one place.
type deps struct {
	client *Client
	log    *slog.Logger
}

// translate maps a newsroom error onto the framework's.
//
// This is the seam that decides status codes. collage.ErrNotFound on a Required
// fragment produces that page's 404; anything else produces its 500. Everything
// depends on the client having kept "no such article" and "backend unreachable"
// apart, which is why it goes to the trouble.
//
// Both errors are wrapped, so errors.Is still finds the newsroom sentinel further
// up — a plugin's error hook can tell an outage from a typo.
func translate(err error) error {
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: %w", collage.ErrNotFound, err)
	}
	return err
}

// base fills the parts of a view every page shares.
func base(rc *collage.RenderContext) *view {
	return &view{
		Site:   site,
		Locale: rc.Locale,
		URL:    urls{Locale: rc.Locale},
	}
}

// pageParam reads "?page=N", defaulting to 1. A reader may have typed it, so
// anything unparseable is page one rather than an error.
func pageParam(rc *collage.RenderContext) int {
	n, err := strconv.Atoi(rc.Request.URL.Query().Get("page"))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// layoutData fills the masthead. It performs no I/O, and that is the point: the
// layout wraps every page including the error pages, so a layout that can fail is a
// layout that can take down the page explaining why the site is broken.
//
// Everything the chrome needs from the backend — the category nav, the sidebar —
// lives in its own fragment with its own fallback, bound into a slot below.
func (d *deps) layoutData(_ context.Context, rc *collage.RenderContext) (*view, []string, error) {
	return base(rc), nil, nil
}

// navData fetches the category list for the masthead. Its failure is contained by a
// fallback, so an unreachable API costs the reader the nav bar and nothing else.
func (d *deps) navData(ctx context.Context, rc *collage.RenderContext) (*view, []string, error) {
	cats, err := d.client.Categories(ctx)
	if err != nil {
		d.log.Warn("site: category nav unavailable", "err", err)
		return nil, nil, err
	}
	v := base(rc)
	v.Nav = cats
	return v, []string{"categories"}, nil
}

// popularData fetches the most-read sidebar. Like the nav it is not required, so
// the article a reader came for still renders when this cannot.
//
// A failure here does more than swap in the fallback: the framework marks the whole
// render degraded and refuses to cache it, so the sidebar returns on the next
// request rather than being frozen into the cached page for the TTL.
func (d *deps) popularData(ctx context.Context, rc *collage.RenderContext) (*view, []string, error) {
	popular, err := d.client.Popular(ctx, 5)
	if err != nil {
		d.log.Warn("site: popular sidebar unavailable", "err", err)
		return nil, nil, err
	}
	v := base(rc)
	v.Popular = popular
	return v, []string{"articles"}, nil
}

// chromeFallbackData supplies the chrome that needs nothing from the backend: every
// fallback, and the error pages' own content. It performs no I/O, because the whole
// point of each of those is that something else already failed.
func (d *deps) chromeFallbackData(_ context.Context, rc *collage.RenderContext) (*view, []string, error) {
	return base(rc), nil, nil
}

// headNotFound and headServerError title the error pages. A 404 that says "The Wire"
// and nothing else is indistinguishable from the front page in a browser's history.
func (d *deps) headNotFound(_ context.Context, rc *collage.RenderContext) (*view, []string, error) {
	v := base(rc)
	v.Heading = localized(rc.Locale, "Not found", "Bulunamadı")
	return v, nil, nil
}

func (d *deps) headServerError(_ context.Context, rc *collage.RenderContext) (*view, []string, error) {
	v := base(rc)
	v.Heading = localized(rc.Locale, "Something went wrong", "Bir şeyler ters gitti")
	return v, nil, nil
}

// The three lookups below are memoised into the render's SharedData.
//
// Each of them is wanted twice in one render: once by the head fragment, which
// needs the headline for the <title>, and once by the content fragment. Fetching
// twice would be correct and would double the request count on every uncached page.
// SharedData exists for exactly this — values exchanged between fragments within a
// single render — and a fragment's per-fragment timeout context shares it, because
// RenderContext.WithContext copies shallowly.
//
// Each falls back to fetching when nothing is stored, so removing the head fragment
// changes the request count and not the behaviour.

func (d *deps) article(ctx context.Context, rc *collage.RenderContext, slug string) (Article, error) {
	if v, ok := rc.Get("article:" + slug); ok {
		if art, ok := v.(Article); ok { // any: SharedData's value type is the framework's
			return art, nil
		}
	}
	art, err := d.client.Article(ctx, slug)
	if err != nil {
		return Article{}, err
	}
	rc.Set("article:"+slug, art)
	return art, nil
}

func (d *deps) category(ctx context.Context, rc *collage.RenderContext, slug string) (Category, error) {
	if v, ok := rc.Get("category:" + slug); ok {
		if cat, ok := v.(Category); ok { // any: SharedData's value type is the framework's
			return cat, nil
		}
	}
	cat, err := d.client.Category(ctx, slug)
	if err != nil {
		return Category{}, err
	}
	rc.Set("category:"+slug, cat)
	return cat, nil
}

func (d *deps) author(ctx context.Context, rc *collage.RenderContext, slug string) (Author, error) {
	if v, ok := rc.Get("author:" + slug); ok {
		if a, ok := v.(Author); ok { // any: SharedData's value type is the framework's
			return a, nil
		}
	}
	a, err := d.client.Author(ctx, slug)
	if err != nil {
		return Author{}, err
	}
	rc.Set("author:"+slug, a)
	return a, nil
}

// The head handlers. Each fills Heading and Standfirst, which partials/head.html
// turns into <title> and the description. They are not required and have a fallback,
// so a page whose metadata cannot be fetched still gets a title rather than losing
// its <head>.

func (d *deps) headHome(_ context.Context, rc *collage.RenderContext) (*view, []string, error) {
	v := base(rc)
	v.Heading = localized(rc.Locale, "Latest", "En yeni")
	v.Standfirst = site.Tagline
	return v, nil, nil
}

func (d *deps) headCategory(ctx context.Context, rc *collage.RenderContext) (*view, []string, error) {
	cat, err := d.category(ctx, rc, rc.Param("slug"))
	if err != nil {
		return nil, nil, translate(err)
	}
	v := base(rc)
	v.Heading = cat.Name
	v.Standfirst = cat.Description
	return v, []string{"categories"}, nil
}

func (d *deps) headAuthor(ctx context.Context, rc *collage.RenderContext) (*view, []string, error) {
	author, err := d.author(ctx, rc, rc.Param("slug"))
	if err != nil {
		return nil, nil, translate(err)
	}
	v := base(rc)
	v.Heading = author.Name
	v.Standfirst = author.Role
	return v, nil, nil
}

func (d *deps) headArticle(ctx context.Context, rc *collage.RenderContext) (*view, []string, error) {
	art, err := d.article(ctx, rc, rc.Param("slug"))
	if err != nil {
		return nil, nil, translate(err)
	}
	v := base(rc)
	v.Heading = art.Title
	v.Standfirst = art.Dek
	return v, []string{"articles"}, nil
}

func (d *deps) headSearch(_ context.Context, rc *collage.RenderContext) (*view, []string, error) {
	v := base(rc)
	v.Heading = localized(rc.Locale, "Search", "Arama")
	if q := rc.Request.URL.Query().Get("q"); q != "" {
		// Not %q: the quotes it adds are escaped to &#34; by the template engine,
		// which renders correctly and reads badly in the page source.
		v.Heading = q + " — " + v.Heading
	}
	v.Standfirst = site.Tagline
	return v, nil, nil
}

// homeData is the front page: the newest articles, paginated.
func (d *deps) homeData(ctx context.Context, rc *collage.RenderContext) (*view, []string, error) {
	listing, err := d.client.Articles(ctx, listQuery{Page: pageParam(rc)})
	if err != nil {
		return nil, nil, translate(err)
	}
	v := base(rc)
	v.Heading = localized(rc.Locale, "Latest", "En yeni")
	v.Standfirst = site.Tagline
	v.Listing = listing
	v.Pager = pager{Path: v.URL.Home()}
	return v, []string{"articles"}, nil
}

// categoryData is a category landing page. It fetches the category itself as well as
// its articles, so an unknown slug is a 404 rather than an empty listing that
// answers 200 and tells a crawler the page exists.
func (d *deps) categoryData(ctx context.Context, rc *collage.RenderContext) (*view, []string, error) {
	slug := rc.Param("slug")

	cat, err := d.category(ctx, rc, slug)
	if err != nil {
		return nil, nil, translate(err)
	}
	listing, err := d.client.Articles(ctx, listQuery{Category: slug, Page: pageParam(rc)})
	if err != nil {
		return nil, nil, translate(err)
	}

	v := base(rc)
	v.Category = &cat
	v.Heading = cat.Name
	v.Standfirst = cat.Description
	v.Listing = listing
	v.Pager = pager{Path: v.URL.Category(slug)}
	return v, []string{"articles", "category:" + slug}, nil
}

// authorData is an author landing page.
func (d *deps) authorData(ctx context.Context, rc *collage.RenderContext) (*view, []string, error) {
	slug := rc.Param("slug")

	author, err := d.author(ctx, rc, slug)
	if err != nil {
		return nil, nil, translate(err)
	}
	listing, err := d.client.Articles(ctx, listQuery{Author: slug, Page: pageParam(rc)})
	if err != nil {
		return nil, nil, translate(err)
	}

	v := base(rc)
	v.Author = &author
	v.Heading = author.Name
	v.Standfirst = author.Role
	v.Listing = listing
	v.Pager = pager{Path: v.URL.Author(slug)}
	return v, []string{"articles", "author:" + slug}, nil
}

// articleData is one piece.
//
// The year and month in the URL are verified against the article rather than
// ignored. Without that check every article would answer at every date, which is a
// duplicate-content problem for crawlers and an unbounded set of cache keys for one
// piece of writing.
func (d *deps) articleData(ctx context.Context, rc *collage.RenderContext) (*view, []string, error) {
	slug := rc.Param("slug")

	art, err := d.article(ctx, rc, slug)
	if err != nil {
		return nil, nil, translate(err)
	}
	if art.Year() != rc.Param("year") || art.Month() != rc.Param("month") {
		return nil, nil, fmt.Errorf("%w: article %q is not dated %s/%s",
			collage.ErrNotFound, slug, rc.Param("year"), rc.Param("month"))
	}

	v := base(rc)
	v.Article = &art
	v.Heading = art.Title
	v.Standfirst = art.Dek
	return v, []string{"articles", "article:" + slug}, nil
}

// searchData answers the search page.
//
// An empty query renders the page with no results rather than listing everything: a
// search box that returns the whole corpus before it is used is a listing page
// wearing a search box.
func (d *deps) searchData(ctx context.Context, rc *collage.RenderContext) (*view, []string, error) {
	query := rc.Request.URL.Query().Get("q")

	v := base(rc)
	v.Query = query
	v.Heading = localized(rc.Locale, "Search", "Arama")
	// The term travels with the page number, or page two is a search for nothing.
	v.Pager = pager{Path: v.URL.Search(), Params: url.Values{"q": {query}}}

	if query == "" {
		v.Standfirst = localized(rc.Locale, "Search headlines, standfirsts and tags.", "Başlıklarda, özetlerde ve etiketlerde arayın.")
		return v, nil, nil
	}

	listing, err := d.client.Articles(ctx, listQuery{Search: query, Page: pageParam(rc)})
	if err != nil {
		return nil, nil, translate(err)
	}
	v.Listing = listing
	// Not localized(): the two languages put the count and the term in opposite
	// orders, so there is no single argument list both format strings can share.
	// English also needs the plural agreed; Turkish does not inflect the noun after
	// a numeral, so "1 sonuç" is already correct.
	switch {
	case rc.Locale == "tr":
		v.Standfirst = fmt.Sprintf("%q için %d sonuç", query, listing.Total)
	case listing.Total == 1:
		v.Standfirst = fmt.Sprintf("1 result for %q", query)
	default:
		v.Standfirst = fmt.Sprintf("%d results for %q", listing.Total, query)
	}
	// No dependency tags: the search page is Dynamic, so nothing caches it and
	// there is nothing for an invalidation to reach.
	return v, nil, nil
}

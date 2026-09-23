package main

import (
	"net/url"
	"strconv"
)

// defaultLocale is the locale served without a path prefix.
const defaultLocale = "en"

// supportedLocales is the set the site registers paths for. Every entry needs a
// path on every page, or that page 404s in that locale.
var supportedLocales = []string{"en", "tr"}

// siteInfo is the masthead. A real deployment reads this from configuration; here
// it is a constant so the layout's data handler stays about the mechanism.
type siteInfo struct {
	Name    string
	Tagline string
}

var site = siteInfo{
	Name:    "The Wire",
	Tagline: "Reporting on systems, science, culture, markets and climate.",
}

// view is what every template in this site renders with. One type covers every
// fragment: the layout reads Site, Nav and URL, the listing pages read Listing, the
// article page reads Article, and the sidebar reads Popular. Fields a given
// template does not use are simply zero.
//
// A single view type rather than one per page is a deliberate trade. It means a
// template can reference a field the handler never filled and render blank instead
// of failing — which is the cost — and it means the layout and the content fragment
// agree on the shape of the chrome without a separate type for every combination,
// which is the benefit. With five page types sharing one layout, the benefit wins.
type view struct {
	// Site is the masthead, read by the layout.
	Site siteInfo
	// Locale is the resolved locale for this render.
	Locale string
	// URL builds locale-correct links. Templates must go through it rather than
	// writing paths inline, or an English URL ends up on a Turkish page.
	URL urls
	// Nav is the category list in the masthead.
	Nav []Category
	// Heading and Standfirst are the page's own title block.
	Heading    string
	Standfirst string
	// Listing is the paginated article list, on the pages that have one.
	Listing Listing
	// Article is the piece being read, on the article page.
	Article *Article
	// Category and Author are the subject of a landing page.
	Category *Category
	// Author is the subject of an author page.
	Author *Author
	// Popular is the most-read sidebar.
	Popular []Article
	// Query is the current search term, echoed into the search box.
	Query string
	// Pager builds the pagination links.
	Pager pager
}

// urls builds every link the templates emit, for one locale.
//
// This exists because the site registers different paths per locale —
// "/category/climate" in English, "/kategori/climate" in Turkish — and a template
// that writes a path inline would emit the English one on a Turkish page and
// produce a 404. Routing that decision through one type means adding a locale is a
// change here rather than a search through every template.
type urls struct {
	// Locale is the locale links are built for.
	Locale string
}

// prefix is the locale path segment. The default locale is served unprefixed, so
// the canonical English URL of the home page is "/" rather than "/en/".
func (u urls) prefix() string {
	if u.Locale == "" || u.Locale == defaultLocale {
		return ""
	}
	return "/" + u.Locale
}

// Home is the front page.
func (u urls) Home() string {
	if p := u.prefix(); p != "" {
		return p + "/"
	}
	return "/"
}

// Category is a category landing page.
func (u urls) Category(slug string) string {
	return u.prefix() + localized(u.Locale, "/category/", "/kategori/") + slug
}

// Author is an author landing page.
func (u urls) Author(slug string) string {
	return u.prefix() + localized(u.Locale, "/author/", "/yazar/") + slug
}

// Search is the search page.
func (u urls) Search() string {
	return u.prefix() + localized(u.Locale, "/search", "/arama")
}

// Article is one piece. The date segments come from the article rather than from a
// formatter here, so the URL a template links and the route the site registers
// cannot drift apart in formatting.
func (u urls) Article(a Article) string {
	return u.prefix() + a.Path()
}

// Switch is the same page in the other locale, used by the language toggle. It
// returns the other locale's home page rather than a translated deep link: this
// site has no mapping from an English slug to a Turkish one, and inventing one that
// silently 404s would be worse than landing the reader on the front page.
func (u urls) Switch() string {
	other := defaultLocale
	if u.Locale == defaultLocale {
		other = supportedLocales[1]
	}
	return urls{Locale: other}.Home()
}

// SwitchLabel names the locale Switch leads to.
func (u urls) SwitchLabel() string {
	if u.Locale == defaultLocale {
		return "Türkçe"
	}
	return "English"
}

// pager builds a listing's pagination links.
//
// It carries Params because a listing path is not always the whole address: the
// search page's results depend on "?q=", and a next link built from the path alone
// drops it, sending the reader to page two of nothing. The fix is to rebuild the
// existing query with only the page number changed.
//
// Params holds what the page itself reads, not whatever the request happened to
// arrive with. Echoing every parameter back into the links would carry a crawler's
// tracking junk through the whole archive, and since the raw query string is part
// of a page's cache key, each variant would be cached separately.
type pager struct {
	// Path is the locale-correct listing path, with no query string.
	Path string
	// Params are the query parameters to preserve across pages. It must not
	// contain "page"; Page sets that.
	Params url.Values
}

// Page is the URL of page n of this listing.
//
// Page one is linked without a "page" parameter, so the first page has one
// canonical URL rather than two that a crawler would index separately and the cache
// would store twice.
func (p pager) Page(n int) string {
	values := url.Values{}
	for key, vs := range p.Params {
		values[key] = vs
	}
	if n > 1 {
		values.Set("page", strconv.Itoa(n))
	}
	if len(values) == 0 {
		return p.Path
	}
	return p.Path + "?" + values.Encode()
}

// localized picks between two values by locale. With two locales and a handful of
// strings, a map per string would be ceremony; if a third locale arrives this is
// the one place that has to change.
func localized(locale, en, tr string) string {
	if locale == "tr" {
		return tr
	}
	return en
}

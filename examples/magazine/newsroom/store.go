package newsroom

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// contentJSON is the magazine's entire corpus. Embedding it is what lets the API
// server run as a single binary with no database and no data directory, which is
// the whole point of a fake backend: it must be trivial to start, or nobody will
// run the example.
//
//go:embed content.json
var contentJSON []byte

// DefaultPerPage is the listing page size used when a request does not ask for one.
const DefaultPerPage = 6

// MaxPerPage caps what a caller may request. Without it, "?per_page=100000" is an
// invitation to serialise the entire corpus on every request, which is a denial of
// service with a query parameter for a trigger.
const MaxPerPage = 24

// Filter selects and paginates a listing. The zero value selects everything, first
// page, default page size.
type Filter struct {
	// Category, when set, restricts the listing to one category slug.
	Category string
	// Author, when set, restricts the listing to one author slug.
	Author string
	// Query, when set, restricts the listing to articles matching it. See
	// Article.Matches for what is searched.
	Query string
	// Page is the 1-based page number. Values below 1 are treated as 1.
	Page int
	// PerPage is the page size, clamped to 1..MaxPerPage. Zero means DefaultPerPage.
	PerPage int
}

// Store holds the corpus in memory and answers the queries the API exposes. It is
// read-only after construction, so its methods are safe to call concurrently
// without synchronisation — there is nothing to synchronise.
type Store struct {
	articles     []Article // newest first; the order every listing inherits
	bySlug       map[string]Article
	categories   []Category
	authors      []Author
	catBySlug    map[string]Category
	authorBySlug map[string]Author
}

// corpus is the on-disk shape of content.json.
type corpus struct {
	Categories []Category `json:"categories"`
	Authors    []Author   `json:"authors"`
	Articles   []Article  `json:"articles"`
}

// NewStore builds a Store from the embedded corpus. It returns an error only if the
// embedded JSON is malformed or internally inconsistent, which is a build-time
// mistake surfaced at startup rather than a runtime condition.
func NewStore() (*Store, error) {
	return LoadStore(contentJSON)
}

// LoadStore builds a Store from raw corpus JSON. It exists so tests can supply a
// corpus of their own without writing files, and so a real deployment could read
// from somewhere else without changing anything below.
//
// Referential integrity is checked here rather than trusted: an article naming a
// category or author that does not exist would otherwise render a landing page link
// that 404s, and finding that at startup is much cheaper than finding it in a
// crawler's report.
func LoadStore(raw []byte) (*Store, error) {
	var c corpus
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("newsroom: parse corpus: %w", err)
	}

	s := &Store{
		categories:   c.Categories,
		authors:      c.Authors,
		bySlug:       make(map[string]Article, len(c.Articles)),
		catBySlug:    make(map[string]Category, len(c.Categories)),
		authorBySlug: make(map[string]Author, len(c.Authors)),
	}
	for _, cat := range c.Categories {
		s.catBySlug[cat.Slug] = cat
	}
	for _, a := range c.Authors {
		s.authorBySlug[a.Slug] = a
	}

	for _, art := range c.Articles {
		if _, ok := s.catBySlug[art.Category]; !ok {
			return nil, fmt.Errorf("newsroom: article %q names unknown category %q", art.Slug, art.Category)
		}
		if _, ok := s.authorBySlug[art.Author]; !ok {
			return nil, fmt.Errorf("newsroom: article %q names unknown author %q", art.Slug, art.Author)
		}
		if _, ok := s.bySlug[art.Slug]; ok {
			return nil, fmt.Errorf("newsroom: duplicate article slug %q", art.Slug)
		}
		s.bySlug[art.Slug] = art
	}

	s.articles = append(s.articles, c.Articles...)
	sort.Slice(s.articles, func(i, j int) bool {
		return s.articles[i].PublishedAt.After(s.articles[j].PublishedAt)
	})
	return s, nil
}

// Article returns one article by slug, or ErrNotFound.
func (s *Store) Article(slug string) (Article, error) {
	art, ok := s.bySlug[slug]
	if !ok {
		return Article{}, fmt.Errorf("%w: article %q", ErrNotFound, slug)
	}
	return art, nil
}

// Category returns one category by slug, or ErrNotFound.
func (s *Store) Category(slug string) (Category, error) {
	cat, ok := s.catBySlug[slug]
	if !ok {
		return Category{}, fmt.Errorf("%w: category %q", ErrNotFound, slug)
	}
	return cat, nil
}

// Author returns one author by slug, or ErrNotFound.
func (s *Store) Author(slug string) (Author, error) {
	a, ok := s.authorBySlug[slug]
	if !ok {
		return Author{}, fmt.Errorf("%w: author %q", ErrNotFound, slug)
	}
	return a, nil
}

// Categories returns every category, in corpus order.
func (s *Store) Categories() []Category {
	out := make([]Category, len(s.categories))
	copy(out, s.categories)
	return out
}

// Authors returns every author, in corpus order.
func (s *Store) Authors() []Author {
	out := make([]Author, len(s.authors))
	copy(out, s.authors)
	return out
}

// List applies f and returns the matching page, newest first.
//
// A page number past the end returns an empty Items with the true Total and
// TotalPages rather than an error. That is a deliberate choice about who handles it:
// the site turns an empty listing into "no articles here", which is the right
// response to "?page=900", whereas an error would have to become a 500 or a 404 and
// neither is true.
func (s *Store) List(f Filter) Page {
	perPage := f.PerPage
	if perPage <= 0 {
		perPage = DefaultPerPage
	}
	if perPage > MaxPerPage {
		perPage = MaxPerPage
	}
	pageNum := f.Page
	if pageNum < 1 {
		pageNum = 1
	}

	matched := make([]Article, 0, len(s.articles))
	for _, art := range s.articles {
		if f.Category != "" && art.Category != f.Category {
			continue
		}
		if f.Author != "" && art.Author != f.Author {
			continue
		}
		if !art.Matches(f.Query) {
			continue
		}
		matched = append(matched, art)
	}

	total := len(matched)
	totalPages := (total + perPage - 1) / perPage

	start := (pageNum - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}

	return Page{
		Items:      matched[start:end],
		Page:       pageNum,
		PerPage:    perPage,
		Total:      total,
		TotalPages: totalPages,
	}
}

// Popular returns the most-read articles, highest first, capped at limit. Ties break
// on slug so the order is stable across calls — an unstable "most read" list makes
// every cached page look different from every other for no reason.
func (s *Store) Popular(limit int) []Article {
	if limit <= 0 {
		limit = 5
	}
	ranked := make([]Article, len(s.articles))
	copy(ranked, s.articles)
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Views != ranked[j].Views {
			return ranked[i].Views > ranked[j].Views
		}
		return strings.Compare(ranked[i].Slug, ranked[j].Slug) < 0
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

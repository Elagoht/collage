// Package newsroom is the magazine example's domain: the article model, the seed
// content both binaries are built from, and the HTTP client the site reads through.
//
// It deliberately does not import the collage framework. The API server has no
// business knowing what renders its JSON, and keeping the boundary honest here is
// what makes the example a demonstration of a site talking to a backend rather than
// a demonstration of a site talking to itself. The site translates this package's
// errors into the framework's at its own edge.
package newsroom

import (
	"errors"
	"strings"
	"time"
)

// ErrNotFound is returned by the store and the client when a slug names nothing.
// The site maps it onto collage.ErrNotFound, which is what turns it into a 404
// rather than a 500.
var ErrNotFound = errors.New("newsroom: not found")

// ErrUnavailable is returned by the client when the API could not be reached or
// answered with a server error. It is deliberately distinct from ErrNotFound: one
// means "there is no such article", the other means "ask again later", and a site
// that conflates them serves 404s during an outage and poisons every crawler that
// sees one.
var ErrUnavailable = errors.New("newsroom: upstream unavailable")

// Article is one published piece.
type Article struct {
	Slug        string    `json:"slug"`
	Title       string    `json:"title"`
	Dek         string    `json:"dek"`
	Body        []string  `json:"body"`
	Category    string    `json:"category"`
	Author      string    `json:"author"`
	PublishedAt time.Time `json:"publishedAt"`
	ReadMinutes int       `json:"readMinutes"`
	Tags        []string  `json:"tags"`
	Views       int       `json:"views"`
}

// Category groups articles and has a landing page of its own.
type Category struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Author writes articles and has a landing page of its own.
type Author struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	Role string `json:"role"`
	Bio  string `json:"bio"`
}

// Page is one page of a listing, carrying the counts a pager needs to render
// itself. The API returns this shape for every listing endpoint.
type Page struct {
	Items      []Article `json:"items"`
	Page       int       `json:"page"`
	PerPage    int       `json:"perPage"`
	Total      int       `json:"total"`
	TotalPages int       `json:"totalPages"`
}

// HasPrev reports whether a previous page exists.
func (p Page) HasPrev() bool { return p.Page > 1 }

// HasNext reports whether a further page exists.
func (p Page) HasNext() bool { return p.Page < p.TotalPages }

// PrevPage is the page number before this one, floored at 1 so a template can link
// it without guarding first.
func (p Page) PrevPage() int {
	if p.Page <= 1 {
		return 1
	}
	return p.Page - 1
}

// NextPage is the page number after this one, capped at the last page for the same
// reason PrevPage is floored.
func (p Page) NextPage() int {
	if p.Page >= p.TotalPages {
		return p.TotalPages
	}
	return p.Page + 1
}

// Year is the article's publication year, as the four-digit string the article URL
// uses. Templates build "/{year}/{month}/{slug}" from these rather than calling
// formatTime, so the URL a template links and the route the site registers cannot
// drift apart in formatting.
func (a Article) Year() string { return a.PublishedAt.Format("2006") }

// Month is the article's zero-padded publication month.
func (a Article) Month() string { return a.PublishedAt.Format("01") }

// Path is the article's canonical URL.
func (a Article) Path() string {
	return "/" + a.Year() + "/" + a.Month() + "/" + a.Slug
}

// Matches reports whether the article satisfies a free-text query, case-insensitively,
// across the fields a reader would expect to search: the headline, the standfirst and
// the tags. The body is deliberately excluded — a magazine search that matches an
// aside in paragraph nine ranks noise above headlines, and this example has no
// ranking to sort it back out with.
func (a Article) Matches(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	if strings.Contains(strings.ToLower(a.Title), q) || strings.Contains(strings.ToLower(a.Dek), q) {
		return true
	}
	for _, tag := range a.Tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return true
		}
	}
	return false
}

package main

// This file is the site's view of the newsroom API: the shape of the JSON it
// returns, and the client that fetches it.
//
// These types are declared here rather than imported from the API's own source,
// even though both happen to live in this repository. A backend does not hand its
// structs to its callers — you read its documentation and declare what you need —
// and a client that imports them is coupled to a repository layout that would not
// exist outside this one. Declaring them separately is also what makes the
// contract visible: everything the site depends on the API for is in this file.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound means the API answered 404: there is no such article, section or
// writer. The site turns this into collage.ErrNotFound, and from there into a 404.
var ErrNotFound = errors.New("newsroom: not found")

// ErrUnavailable means the API could not be reached, or answered with a server
// error.
//
// Keeping this apart from ErrNotFound is the single most important thing this file
// does. A site that conflates them answers 404 during an outage, which tells every
// crawler that reaches it that the archive has been deleted.
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

// Listing is one page of articles, with the counts a pager needs to render itself.
type Listing struct {
	Items      []Article `json:"items"`
	Page       int       `json:"page"`
	PerPage    int       `json:"perPage"`
	Total      int       `json:"total"`
	TotalPages int       `json:"totalPages"`
}

// HasPrev reports whether a previous page exists.
func (l Listing) HasPrev() bool { return l.Page > 1 }

// HasNext reports whether a further page exists.
func (l Listing) HasNext() bool { return l.Page < l.TotalPages }

// PrevPage is the page before this one, floored at 1 so a template can link it
// without guarding first.
func (l Listing) PrevPage() int {
	if l.Page <= 1 {
		return 1
	}
	return l.Page - 1
}

// NextPage is the page after this one, capped at the last for the same reason.
func (l Listing) NextPage() int {
	if l.Page >= l.TotalPages {
		return l.TotalPages
	}
	return l.Page + 1
}

// Year is the article's publication year, as the four-digit string the article URL
// uses. Templates build the URL from these rather than formatting a date inline, so
// the link a template emits and the route the site registers cannot drift apart.
func (a Article) Year() string { return a.PublishedAt.Format("2006") }

// Month is the article's zero-padded publication month.
func (a Article) Month() string { return a.PublishedAt.Format("01") }

// Path is the article's canonical path, without a locale prefix.
func (a Article) Path() string { return "/" + a.Year() + "/" + a.Month() + "/" + a.Slug }

// listQuery selects one page of a listing. The zero value asks for everything, first
// page, the API's default page size.
type listQuery struct {
	Category string
	Author   string
	Search   string
	Page     int
	PerPage  int
}

// requestTimeout bounds a single attempt, not the whole call. With retries the
// worst case is roughly Attempts × (requestTimeout + RetryDelay), which must stay
// under the fragment timeout the site gives its data handlers — otherwise the
// render gives up before the retry that would have succeeded.
const requestTimeout = 2 * time.Second

// Client reads the newsroom API over HTTP.
type Client struct {
	// BaseURL is the API root, without a trailing slash.
	BaseURL string
	// HTTP is the underlying client. Nil means one with requestTimeout.
	HTTP *http.Client
	// Attempts is the total number of tries per call, including the first. It is
	// deliberately small: a site that retries three times against a backend that is
	// down turns one slow page into three, and under load that is how a struggling
	// backend is finished off.
	Attempts int
	// RetryDelay is the pause between attempts.
	RetryDelay time.Duration
}

// NewClient returns a Client pointed at baseURL with the defaults filled in.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTP:       &http.Client{Timeout: requestTimeout},
		Attempts:   2,
		RetryDelay: 150 * time.Millisecond,
	}
}

// Articles returns one page of the listing selected by q.
func (c *Client) Articles(ctx context.Context, q listQuery) (Listing, error) {
	values := url.Values{}
	if q.Category != "" {
		values.Set("category", q.Category)
	}
	if q.Author != "" {
		values.Set("author", q.Author)
	}
	if q.Search != "" {
		values.Set("q", q.Search)
	}
	if q.Page > 0 {
		values.Set("page", strconv.Itoa(q.Page))
	}
	if q.PerPage > 0 {
		values.Set("per_page", strconv.Itoa(q.PerPage))
	}
	return fetch[Listing](ctx, c, "/v1/articles", values)
}

// Article returns one article by slug, or an error wrapping ErrNotFound.
func (c *Client) Article(ctx context.Context, slug string) (Article, error) {
	return fetch[Article](ctx, c, "/v1/articles/"+url.PathEscape(slug), nil)
}

// Categories returns every section.
func (c *Client) Categories(ctx context.Context) ([]Category, error) {
	return fetch[[]Category](ctx, c, "/v1/categories", nil)
}

// Category returns one section by slug, or an error wrapping ErrNotFound.
func (c *Client) Category(ctx context.Context, slug string) (Category, error) {
	return fetch[Category](ctx, c, "/v1/categories/"+url.PathEscape(slug), nil)
}

// Author returns one writer by slug, or an error wrapping ErrNotFound.
func (c *Client) Author(ctx context.Context, slug string) (Author, error) {
	return fetch[Author](ctx, c, "/v1/authors/"+url.PathEscape(slug), nil)
}

// Popular returns the most-read articles, highest first.
func (c *Client) Popular(ctx context.Context, limit int) ([]Article, error) {
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	return fetch[[]Article](ctx, c, "/v1/popular", values)
}

// Health reports whether the API answers. It does not retry: a readiness check that
// retries reports the state of the last few seconds rather than of now.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/healthz", nil)
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrUnavailable, err)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: healthz returned %d", ErrUnavailable, resp.StatusCode)
	}
	return nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: requestTimeout}
}

// fetch performs one GET and decodes the body into T, retrying only what retrying
// can fix.
//
// Two conditions besides success stop the loop. A 4xx is a statement about the
// request, and sending it again produces the same statement. A cancelled context
// means the caller has already given up — usually the fragment timeout firing — and
// continuing to sleep and retry after that burns the backend's capacity on a
// response nobody will read.
func fetch[T any](ctx context.Context, c *Client, path string, values url.Values) (T, error) {
	var zero T

	endpoint := c.BaseURL + path
	if len(values) > 0 {
		endpoint += "?" + values.Encode()
	}

	attempts := c.Attempts
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := range attempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return zero, fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
			case <-time.After(c.RetryDelay):
			}
		}

		value, err := fetchOnce[T](ctx, c, endpoint)
		if err == nil {
			return value, nil
		}
		lastErr = err

		if isClientError(err) {
			return zero, err
		}
		if ctx.Err() != nil {
			return zero, err
		}
	}
	return zero, lastErr
}

// clientError marks a 4xx so fetch can tell "your request was wrong" apart from
// "the backend is unwell" without inspecting status codes a second time. It
// unwraps, so errors.Is(err, ErrNotFound) still works through it.
type clientError struct{ err error }

func (e clientError) Error() string { return e.err.Error() }
func (e clientError) Unwrap() error { return e.err }

func isClientError(err error) bool {
	var ce clientError
	return errors.As(err, &ce)
}

func fetchOnce[T any](ctx context.Context, c *Client, endpoint string) (T, error) {
	var zero T

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return zero, clientError{fmt.Errorf("newsroom: build request for %s: %w", endpoint, err)}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return zero, fmt.Errorf("%w: GET %s: %v", ErrUnavailable, endpoint, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// Drained so the connection can be reused. On a site with reader-editable
		// URLs a 404 is common enough that leaking one connection per miss matters.
		io.Copy(io.Discard, resp.Body)
		return zero, clientError{fmt.Errorf("%w: GET %s", ErrNotFound, endpoint)}
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		io.Copy(io.Discard, resp.Body)
		return zero, clientError{fmt.Errorf("newsroom: GET %s: %s", endpoint, resp.Status)}
	case resp.StatusCode != http.StatusOK:
		io.Copy(io.Discard, resp.Body)
		return zero, fmt.Errorf("%w: GET %s: %s", ErrUnavailable, endpoint, resp.Status)
	}

	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return zero, fmt.Errorf("%w: decode %s: %v", ErrUnavailable, endpoint, err)
	}
	return out, nil
}

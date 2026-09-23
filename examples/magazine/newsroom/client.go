package newsroom

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

// DefaultTimeout bounds a single attempt, not the whole call. With retries, the
// worst case is roughly Attempts × (DefaultTimeout + RetryDelay), which must stay
// comfortably under the fragment timeout the site gives its data handlers or the
// retry is pointless: the render gives up before the second attempt returns.
const DefaultTimeout = 2 * time.Second

// Client reads the magazine API over HTTP.
//
// Two behaviours here are the reason this is a type rather than a few functions.
// First, it distinguishes "no such thing" from "cannot reach the backend", because
// the site must answer 404 for one and 503-with-a-cached-page for the other.
// Second, it retries, but only what is worth retrying: a 404 is a fact and retrying
// it just multiplies the latency of every mistyped URL.
type Client struct {
	// BaseURL is the API root, without a trailing slash.
	BaseURL string
	// HTTP is the underlying client. A nil value means a client with DefaultTimeout.
	HTTP *http.Client
	// Attempts is the total number of tries per call, including the first. Values
	// below 1 mean 1. It is deliberately small: a site that retries three times
	// against a backend that is down turns one slow page into three, and under load
	// that is how a struggling backend is finished off.
	Attempts int
	// RetryDelay is the pause between attempts.
	RetryDelay time.Duration
}

// NewClient returns a Client pointed at baseURL with the defaults filled in.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTP:       &http.Client{Timeout: DefaultTimeout},
		Attempts:   2,
		RetryDelay: 150 * time.Millisecond,
	}
}

// Articles returns one page of the listing selected by f.
func (c *Client) Articles(ctx context.Context, f Filter) (Page, error) {
	q := url.Values{}
	if f.Category != "" {
		q.Set("category", f.Category)
	}
	if f.Author != "" {
		q.Set("author", f.Author)
	}
	if f.Query != "" {
		q.Set("q", f.Query)
	}
	if f.Page > 0 {
		q.Set("page", strconv.Itoa(f.Page))
	}
	if f.PerPage > 0 {
		q.Set("per_page", strconv.Itoa(f.PerPage))
	}
	return fetch[Page](ctx, c, "/v1/articles", q)
}

// Article returns one article by slug, or an error wrapping ErrNotFound.
func (c *Client) Article(ctx context.Context, slug string) (Article, error) {
	return fetch[Article](ctx, c, "/v1/articles/"+url.PathEscape(slug), nil)
}

// Categories returns every category.
func (c *Client) Categories(ctx context.Context) ([]Category, error) {
	return fetch[[]Category](ctx, c, "/v1/categories", nil)
}

// Category returns one category by slug, or an error wrapping ErrNotFound.
func (c *Client) Category(ctx context.Context, slug string) (Category, error) {
	return fetch[Category](ctx, c, "/v1/categories/"+url.PathEscape(slug), nil)
}

// Author returns one author by slug, or an error wrapping ErrNotFound.
func (c *Client) Author(ctx context.Context, slug string) (Author, error) {
	return fetch[Author](ctx, c, "/v1/authors/"+url.PathEscape(slug), nil)
}

// Popular returns the most-read articles, highest first.
func (c *Client) Popular(ctx context.Context, limit int) ([]Article, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return fetch[[]Article](ctx, c, "/v1/popular", q)
}

// Health reports whether the API answers. It is what the site's own readiness check
// is built on, and it does not retry: a readiness probe that retries reports the
// state of the last few seconds rather than of now.
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
	return &http.Client{Timeout: DefaultTimeout}
}

// fetch performs one GET and decodes the body into T, retrying only what retrying
// can fix.
//
// The retry loop stops on two conditions besides success. A 4xx is a statement about
// the request, and sending it again produces the same statement, so it returns
// immediately. A cancelled context means the caller has already given up — usually
// the fragment timeout firing — and continuing to sleep and retry after that point
// burns the backend's capacity on a response nobody will read.
func fetch[T any](ctx context.Context, c *Client, path string, query url.Values) (T, error) {
	var zero T

	endpoint := c.BaseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
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

		// A fact about the request, not about the backend's mood.
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
		// Drained so the connection can be reused; a 404 is common enough on a
		// site with user-editable URLs that leaking one connection per miss
		// matters.
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
		// A truncated body is an upstream problem and worth another attempt; a
		// structurally wrong one is not, but they are indistinguishable here and
		// the retry budget is small enough that guessing wrong is cheap.
		return zero, fmt.Errorf("%w: decode %s: %v", ErrUnavailable, endpoint, err)
	}
	return out, nil
}

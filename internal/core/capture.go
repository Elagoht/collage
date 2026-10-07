package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"

	"github.com/Elagoht/collage/internal/types"
)

// requestSpecific are headers that describe one response, never the file a
// static host serves, in canonical form. X-Collage-Render-Time is development's
// render timing: a cache miss carries it and the hit after it does not.
var requestSpecific = []string{
	"Date", "Etag", "Last-Modified", "Content-Length", "Set-Cookie", "Vary",
	"Content-Encoding", "Transfer-Encoding", "Connection", "Age",
	"X-Collage-Render-Time",
}

// CaptureResponses asks the application's handler for each path twice, in
// process, as a static export would be asked for it — GET, no cookies, no
// Accept-Encoding, the host of Config.BaseURL or "localhost" — and keeps the
// headers both responses agree on. A header the two responses disagree on, or
// that only one of them carries, is named in Unstable instead.
//
// It starts the application, as Handler does, and returns the error startup
// produced rather than capturing a 503 for every path. The paths are asked for
// one at a time.
func (a *App) CaptureResponses(ctx context.Context, paths []string) (map[string]types.CapturedResponse, error) {
	handler, err := a.buildHandler()
	if err != nil {
		return nil, err
	}
	host := "localhost"
	if u, err := url.Parse(a.cfg.BaseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	out := make(map[string]types.CapturedResponse, len(paths))
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		asked := p
		first := captureOnce(ctx, handler, host, asked)
		if other, ok := slashSpelling(first, asked); ok {
			// The page is answered at its other spelling — "/about/" under
			// TrailingSlash, which is how the static host serves about/index.html
			// too — and its headers are that answer's, not the redirect's.
			asked = other
			first = captureOnce(ctx, handler, host, asked)
		}
		second := captureOnce(ctx, handler, host, asked)
		out[p] = compareResponses(first, second)
	}
	return out, nil
}

// compareResponses keeps what first and second agree on, past the
// request-specific headers, with first's status. The headers are each
// response's as written with its status — Result's snapshot — not whatever a
// handler set on the map afterwards, which no client is sent.
func compareResponses(firstRec, secondRec *httptest.ResponseRecorder) types.CapturedResponse {
	first, second := firstRec.Result().Header, secondRec.Result().Header
	kept := http.Header{}
	var unstable []string
	for name, values := range first {
		if slices.Contains(requestSpecific, name) {
			continue
		}
		if !slices.Equal(values, second[name]) {
			unstable = append(unstable, name)
			continue
		}
		kept[name] = slices.Clone(values)
	}
	for name := range second {
		if _, ok := first[name]; ok || slices.Contains(requestSpecific, name) {
			continue
		}
		unstable = append(unstable, name)
	}
	slices.Sort(unstable)
	return types.CapturedResponse{Status: firstRec.Code, Headers: kept, Unstable: unstable}
}

// slashSpelling reports the spelling rec redirected urlPath to, when that
// redirect is the router's trailing-slash canonicalisation: urlPath with a "/"
// added or taken away, and nothing else.
func slashSpelling(rec *httptest.ResponseRecorder, urlPath string) (string, bool) {
	if rec.Code != http.StatusMovedPermanently {
		return "", false
	}
	location := rec.Header().Get("Location")
	u, err := url.Parse(location)
	if err != nil || u.Host != "" || u.Scheme != "" || u.RawQuery != "" {
		return "", false
	}
	if u.Path == urlPath+"/" || (urlPath != "/" && u.Path == strings.TrimSuffix(urlPath, "/") && strings.HasSuffix(urlPath, "/")) {
		return u.Path, true
	}
	return "", false
}

// captureOnce serves one GET for urlPath through handler. The request is built
// from a URL rather than parsed from a request line, so a path with a space or a
// non-ASCII byte in it is asked for as it is rather than refused.
func captureOnce(ctx context.Context, handler http.Handler, host, urlPath string) *httptest.ResponseRecorder {
	u := &url.URL{Path: urlPath}
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	req.URL = u
	req.RequestURI = u.RequestURI()
	req.Host = host
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

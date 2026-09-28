package httpx

import (
	"net/http"
	"strings"
)

// sharedRequest returns the request a render stored in the cache is made from:
// r, with only what the cache key holds.
//
// One render of a cacheable page is served to every reader whose request has
// its key, so the render must not see what differs between them. It used to be
// handed the first reader's whole request — their cookie, their credentials,
// their address, every query parameter — and a handler that read one wrote it
// into the copy everyone after was given: a session shown to strangers, a
// tracking parameter frozen into a page. The promise a cacheable page makes,
// that its output is the same for everyone, is kept here instead of trusted.
//
// What stays is what the key is made of: the path, the host, the query
// parameters the page names with CacheParams (all of them when it names none,
// since then the whole query is in the key), and the headers middleware declared
// with collage.Vary. The context stays too, values and all: what middleware put
// there is the application's own, and the locale and the trace live in it.
func sharedRequest(r *http.Request, cacheParams []string) *http.Request {
	shared := r.Clone(r.Context())
	shared.Header = make(http.Header)
	for _, name := range requestVaryHeaders(r) {
		key := http.CanonicalHeaderKey(name)
		if values, ok := r.Header[key]; ok {
			shared.Header[key] = append([]string(nil), values...)
		}
	}
	if cacheParams != nil {
		shared.URL.RawQuery = strings.Join(queryVary(r.URL, cacheParams), "")
		shared.RequestURI = shared.URL.RequestURI()
	}
	shared.Form, shared.PostForm, shared.MultipartForm = nil, nil, nil
	shared.Body, shared.ContentLength, shared.Trailer = http.NoBody, 0, nil
	shared.RemoteAddr = ""
	if r.TLS != nil {
		// Still TLS, so a scheme reads right; not the reader's certificate.
		state := *r.TLS
		state.PeerCertificates, state.VerifiedChains = nil, nil
		shared.TLS = &state
	}
	return shared
}

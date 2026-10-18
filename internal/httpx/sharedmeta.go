package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/observability"
)

// sharedStoreTimeout bounds each call the page and document paths make to a
// SharedCache on a request's behalf: the epoch read before a render, and the
// write after it. It is datacache.BackendTimeout's budget, for the same reason:
// a cache is there to make a page faster, and one slower than this is treated as
// down for that call — no epoch, so no write; or a write not made — rather than
// let a stalled connection hold every cacheable page.
const sharedStoreTimeout = 500 * time.Millisecond

// sharedMeta is the meta a page or document entry is written with to a
// SharedCache: where the entry lives, so any instance can purge it from a CDN,
// and its recipe, so any instance can render it again. The store keeps it
// opaque and hands it back with each entry a DropTags reached.
//
// Every string is carried as bytes, which JSON writes as base64: a JSON string
// replaces invalid UTF-8 with U+FFFD, and a path such as /a%FF, a query or a
// header value with obs-text would come back as another URL, so another key.
type sharedMeta struct {
	Host   []byte `json:"host"`
	Path   []byte `json:"path"`
	Recipe recipe `json:"recipe"`
}

// encodeSharedMeta returns the meta an entry for host and path, written from
// rcp, is stored with.
func encodeSharedMeta(host, path string, rcp recipe) []byte {
	// Nothing in a sharedMeta can fail to marshal: strings, bools, and slices
	// and a map of strings.
	meta, _ := json.Marshal(sharedMeta{Host: []byte(host), Path: []byte(path), Recipe: rcp})
	return meta
}

// decodeSharedMeta is the inverse of encodeSharedMeta. A meta that does not
// decode, or names no path, is an error: it was not written by this path.
func decodeSharedMeta(meta []byte) (sharedMeta, error) {
	var m sharedMeta
	if err := json.Unmarshal(meta, &m); err != nil {
		return sharedMeta{}, err
	}
	if len(m.Path) == 0 {
		return sharedMeta{}, fmt.Errorf("collage: shared meta names no path")
	}
	return m, nil
}

// DecodeSharedMeta returns the host and path a page or document entry's meta —
// as a SharedCache hands it back in a cache.Reached — names, and whether it is
// meta this package wrote. The host is as the request carried it, not lowered.
func DecodeSharedMeta(meta []byte) (host, path string, ok bool) {
	m, err := decodeSharedMeta(meta)
	if err != nil {
		return "", "", false
	}
	return string(m.Host), string(m.Path), true
}

// recipeJSON is a recipe's wire form. Every field is written, and every string
// as bytes, byte-exact (see sharedMeta). cacheParams has no omitempty: queryVary
// tells a route that names no CacheParams (nil, null) from one that names an
// empty list ([]), and so must the round trip. Header names stay strings: a
// request cannot carry one that is not a token, which is ASCII.
type recipeJSON struct {
	Host        []byte              `json:"host"`
	Path        []byte              `json:"path"`
	RawPath     []byte              `json:"rawPath"`
	Query       []byte              `json:"query"`
	Vary        [][]byte            `json:"vary"`
	Header      map[string][][]byte `json:"header"`
	TLS         bool                `json:"tls"`
	Document    bool                `json:"document"`
	CacheParams [][]byte            `json:"cacheParams"`
}

// toBytes returns each of values as bytes, keeping a nil nil and an empty list
// empty.
func toBytes(values []string) [][]byte {
	if values == nil {
		return nil
	}
	out := make([][]byte, len(values))
	for i, v := range values {
		out[i] = []byte(v)
	}
	return out
}

// fromBytes is toBytes's inverse.
func fromBytes(values [][]byte) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

// MarshalJSON writes every field of the recipe. On the value, so a recipe held
// by value in a sharedMeta is written through it.
func (rc recipe) MarshalJSON() ([]byte, error) {
	var header map[string][][]byte
	if rc.header != nil {
		header = make(map[string][][]byte, len(rc.header))
		for name, values := range rc.header {
			header[name] = toBytes(values)
		}
	}
	return json.Marshal(recipeJSON{
		Host:        []byte(rc.host),
		Path:        []byte(rc.path),
		RawPath:     []byte(rc.rawPath),
		Query:       []byte(rc.query),
		Vary:        toBytes(rc.vary),
		Header:      header,
		TLS:         rc.tls,
		Document:    rc.document,
		CacheParams: toBytes(rc.cacheParams),
	})
}

// UnmarshalJSON reads what MarshalJSON wrote.
func (rc *recipe) UnmarshalJSON(data []byte) error {
	var wire recipeJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var header http.Header
	if wire.Header != nil {
		header = make(http.Header, len(wire.Header))
		for name, values := range wire.Header {
			header[name] = fromBytes(values)
		}
	}
	*rc = recipe{
		host:        string(wire.Host),
		path:        string(wire.Path),
		rawPath:     string(wire.RawPath),
		query:       string(wire.Query),
		vary:        fromBytes(wire.Vary),
		header:      header,
		tls:         wire.TLS,
		document:    wire.Document,
		cacheParams: fromBytes(wire.CacheParams),
	}
	return nil
}

// AdoptShared takes on the recipes of the page and document entries a
// SharedCache's DropTags reached, which another instance may have written: for
// each key whose recipe this instance lacks, the recipe in its meta is kept,
// marked read, so a warm of it renders it here. A recipe already held is left
// as it is — this instance wrote or served the key itself, so its own is at
// least as fresh, and its read mark is its own.
//
// It returns the keys whose recipes it now holds, in reached's order: those it
// held already and those it adopted. An entry whose meta does not decode is
// skipped unless its recipe was held already.
func (h *Handler) AdoptShared(reached []cache.Reached) []string {
	held := make([]string, 0, len(reached))
	for _, entry := range reached {
		if _, ok := h.recipes.get(entry.Key); ok {
			held = append(held, entry.Key)
			continue
		}
		meta, err := decodeSharedMeta(entry.Meta)
		if err != nil {
			continue
		}
		h.recipes.putIfAbsent(entry.Key, meta.Recipe)
		held = append(held, entry.Key)
	}
	return held
}

// writeShared is writeCache's and writeDocumentCache's write to a SharedCache,
// after their capture check: a conditional write that weighs every instance's
// invalidations, not only this process's. tags are the entry's own; the route's
// path and host tags are added here, so the store's index — which any instance
// invalidates by — carries them as the tracker does. rcp is how to rebuild the
// request, written into the entry's meta whether or not this instance warms, as
// another that does may adopt it (see AdoptShared). report reports a failure
// of the tracker the way the caller's path does.
//
// The store's own failures — the write, and the mark of an overlapped one —
// go to the Handler's SharedHealth instead, not to report: a SharedCache's
// failure is a write not made, never the request's, and a full Redis, which
// answers the epoch read and fails every write, would otherwise log an error
// and send plugins an ErrorEvent on every cacheable miss. The health logs it
// once per window, with the data cache's failures.
//
// mark.shared is the store's epoch read before the render began. Without one —
// the store was unreachable then — the write cannot be weighed, so it is not
// made, and not reported: the read already found the store down. A write that
// an invalidation overlapped lands stale, still the best copy to fall back on;
// one a purge overlapped is not stored. Either way the response is still
// served, and an empty ETag is returned.
func (h *Handler) writeShared(r *http.Request, shared cache.SharedCache, key string, content []byte, ttl time.Duration, tags []string, rcp recipe, mark invalidationMark, report func(error)) string {
	ctx := r.Context()
	if !mark.sharedOK {
		return ""
	}
	routeTags := withRouteTags(tags, r.Host, r.URL.Path)

	// Not cancelled with the request: the render is done, and the reader who
	// waited for it leaving is no reason to lose it for the next one.
	// detached, not writeCtx, is whose end would make a failure no news: the
	// write's own budget running out is the store failing.
	detached := context.WithoutCancel(ctx)
	writeCtx, cancel := context.WithTimeout(detached, sharedStoreTimeout)
	defer cancel()
	etag, outcome, err := shared.SetSince(writeCtx, key, content, ttl, routeTags, encodeSharedMeta(r.Host, r.URL.Path, rcp), mark.shared)
	h.sharedHealth.Done(detached, "page write", key, err)
	if err != nil {
		return ""
	}
	switch outcome {
	case cache.WriteSkipped:
		h.overlapped(ctx, mark)
		return ""
	case cache.WriteStale:
		h.overlapped(ctx, mark)
		h.wroteForWarm(ctx, key, true)
		return ""
	}

	h.metrics.CacheEvent(ctx, observability.CacheSet, key)
	// See writeCache.
	if h.warm != nil {
		h.recipes.put(key, rcp, warmStateOf(ctx) == nil)
	}
	if err := h.tracker.Track(ctx, key, routeTags); err != nil {
		report(fmt.Errorf("collage: track %q: %w", key, err))
	}
	if h.staleIfOverlapped(ctx, shared, key, mark) {
		h.wroteForWarm(ctx, key, true)
		return ""
	}
	h.wroteForWarm(ctx, key, false)
	return etag
}

// staleIfOverlapped is dropIfOverlapped for a SharedCache's fresh write: on any
// local overlap — under way as the render began as well as begun since, the
// check the other stores make before the write, which writeShared leaves to the
// store's epoch and to this — the entry is marked stale rather than dropped.
// The store has already dropped what any instance's purge reached; what is left
// is the best copy to fall back on.
//
// A failure to mark it goes to the SharedHealth, as a failed write does: the
// entry would otherwise stay fresh for its TTL, forever for a Static page, so
// it is logged, but once per window, as the store failing, not as a request's
// error. Bounded and not cancelled with the request, as the write is.
//
// It reports whether the write was overlapped.
func (h *Handler) staleIfOverlapped(ctx context.Context, shared cache.SharedCache, key string, mark invalidationMark) bool {
	if h.storable(mark) {
		return false
	}
	detached := context.WithoutCancel(ctx)
	markCtx, cancel := context.WithTimeout(detached, sharedStoreTimeout)
	defer cancel()
	h.sharedHealth.Done(detached, "page mark stale", key, shared.MarkStaleKey(markCtx, key))
	h.overlapped(ctx, mark)
	return true
}

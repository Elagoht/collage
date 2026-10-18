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
type sharedMeta struct {
	Host   string `json:"host"`
	Path   string `json:"path"`
	Recipe recipe `json:"recipe"`
}

// encodeSharedMeta returns the meta an entry for host and path, written from
// rcp, is stored with.
func encodeSharedMeta(host, path string, rcp recipe) []byte {
	// Nothing in a sharedMeta can fail to marshal: strings, bools, and slices
	// and a map of strings.
	meta, _ := json.Marshal(sharedMeta{Host: host, Path: path, Recipe: rcp})
	return meta
}

// decodeSharedMeta is the inverse of encodeSharedMeta. A meta that does not
// decode, or names no path, is an error: it was not written by this path.
func decodeSharedMeta(meta []byte) (sharedMeta, error) {
	var m sharedMeta
	if err := json.Unmarshal(meta, &m); err != nil {
		return sharedMeta{}, err
	}
	if m.Path == "" {
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
	return m.Host, m.Path, true
}

// recipeJSON is a recipe's wire form. Every field is written, cacheParams with
// no omitempty: queryVary tells a route that names no CacheParams (nil, null)
// from one that names an empty list ([]), and so must the round trip.
type recipeJSON struct {
	Host        string      `json:"host"`
	Path        string      `json:"path"`
	RawPath     string      `json:"rawPath"`
	Query       string      `json:"query"`
	Vary        []string    `json:"vary"`
	Header      http.Header `json:"header"`
	TLS         bool        `json:"tls"`
	Document    bool        `json:"document"`
	CacheParams []string    `json:"cacheParams"`
}

// MarshalJSON writes every field of the recipe. On the value, so a recipe held
// by value in a sharedMeta is written through it.
func (rc recipe) MarshalJSON() ([]byte, error) {
	return json.Marshal(recipeJSON{
		Host:        rc.host,
		Path:        rc.path,
		RawPath:     rc.rawPath,
		Query:       rc.query,
		Vary:        rc.vary,
		Header:      rc.header,
		TLS:         rc.tls,
		Document:    rc.document,
		CacheParams: rc.cacheParams,
	})
}

// UnmarshalJSON reads what MarshalJSON wrote.
func (rc *recipe) UnmarshalJSON(data []byte) error {
	var wire recipeJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*rc = recipe{
		host:        wire.Host,
		path:        wire.Path,
		rawPath:     wire.RawPath,
		query:       wire.Query,
		vary:        wire.Vary,
		header:      wire.Header,
		tls:         wire.TLS,
		document:    wire.Document,
		cacheParams: wire.CacheParams,
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
// the way the caller's path does.
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
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedStoreTimeout)
	defer cancel()
	etag, outcome, err := shared.SetSince(writeCtx, key, content, ttl, routeTags, encodeSharedMeta(r.Host, r.URL.Path, rcp), mark.shared)
	if err != nil {
		report(fmt.Errorf("collage: cache write: %w", err))
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
	if h.dropIfOverlapped(ctx, key, mark) {
		h.wroteForWarm(ctx, key, true)
		return ""
	}
	h.wroteForWarm(ctx, key, false)
	return etag
}

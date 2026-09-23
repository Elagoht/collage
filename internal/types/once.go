package types

import "context"

// onceCall is one Once in progress or finished.
type onceCall struct {
	done  chan struct{}
	value any // any: Once is generic over the caller's type; the table that holds results is not
	err   error
}

// Once runs fetch at most once per render for a given key, and hands its result to
// every fragment that asks for the same key.
//
// It exists because the obvious way to share work between fragments has a hole in
// it. The usual shape —
//
//	if v, ok := rc.Get(key); ok { return v.(Article), nil }
//	article, err := api.Article(ctx, slug)
//	rc.Set(key, article)
//
// is a check and then an act, with the fetch in between. When one fragment ran at a
// time that was fine. Sibling fragments now run concurrently, so two of them can
// both miss the Get and both make the same call: the page renders correctly and
// quietly asks the upstream twice for one article.
//
// Once closes the gap. The first caller for a key fetches; the rest wait for it and
// receive what it produced, errors included — a failed fetch is a result, and
// retrying it once per fragment is how one slow failure becomes several.
//
// The result lives exactly as long as the render. There is no eviction policy
// because there is nothing to evict: the table goes away with the request. What
// outlives a request belongs in the page cache, not here.
//
// A caller whose own context ends stops waiting and returns that error, rather than
// holding a fragment open for work that is no longer wanted.
func Once[T any](rc *RenderContext, key string, fetch func(context.Context) (T, error)) (T, error) {
	var zero T
	if rc == nil || rc.state == nil {
		// No render to share within: fetching directly is the honest answer, and
		// is what a hand-built RenderContext in a test would want.
		if fetch == nil {
			return zero, nil
		}
		return fetch(context.Background())
	}

	rc.state.mu.Lock()
	if call, running := rc.state.once[key]; running {
		rc.state.mu.Unlock()

		select {
		case <-call.done:
		case <-rc.Context().Done():
			return zero, rc.Context().Err()
		}

		if call.err != nil {
			return zero, call.err
		}
		value, ok := call.value.(T)
		if !ok {
			// The same key asked for as two different types. Returning the zero
			// value of the wrong type would be a silently empty section; saying so
			// names the fragment that disagreed.
			return zero, ErrOnceTypeMismatch
		}
		return value, nil
	}

	call := &onceCall{done: make(chan struct{})}
	rc.state.once[key] = call
	rc.state.mu.Unlock()

	if fetch != nil {
		value, err := fetch(rc.Context())
		call.value, call.err = value, err
	}
	close(call.done)

	if call.err != nil {
		return zero, call.err
	}
	value, _ := call.value.(T)
	return value, nil
}

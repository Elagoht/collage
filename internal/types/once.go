package types

import (
	"context"
	"errors"
	"fmt"
)

// onceCall is one Once in progress or finished.
type onceCall struct {
	done  chan struct{}
	value any // any: Once is generic over the caller's type; the table that holds results is not
	err   error
	// abandoned is set when the fetch ended because its own caller's context did:
	// not a result to share, see runOnce.
	abandoned bool
}

// Once runs fetch at most once per render for a given key, and hands its result to
// every fragment that asks for the same key. A key is its name and its type, so
// two keys of one name and different types are two fetches.
//
// It exists because the obvious way to share work between fragments has a hole in
// it. The usual shape —
//
//	if article, ok := articleKey.With(slug).Get(rc); ok { return article, nil }
//	article, err := api.Article(ctx, slug)
//	articleKey.With(slug).Set(rc, article)
//
// is a check and then an act, with the fetch in between. When one fragment ran at a
// time that was fine. Sibling fragments now run concurrently, so two of them can
// both miss the Get and both make the same call: the page renders correctly and
// quietly asks the upstream twice for one article.
//
// Once closes the gap. The first caller for a key fetches; the rest wait for it and
// receive what it produced, errors included — a failed fetch is a result, and
// retrying it once per fragment is how one slow failure becomes several. The one
// exception is a fetch that failed only because its own caller was cancelled: that
// says nothing about the key, so the next caller fetches again.
//
// The result lives exactly as long as the render. There is no eviction policy
// because there is nothing to evict: the table goes away with the request. What
// outlives a request belongs in the page cache, not here.
//
// A caller whose own context ends stops waiting and returns that error, rather than
// holding a fragment open for work that is no longer wanted.
func Once[T any](rc *RenderContext, key Key[T], fetch func(context.Context) (T, error)) (T, error) {
	var zero T
	id := key.id()
	if rc == nil || rc.state == nil {
		// No render to share within: fetching directly is the honest answer, and
		// is what a hand-built RenderContext in a test would want.
		if fetch == nil {
			return zero, nil
		}
		return fetch(context.Background())
	}

	for {
		rc.state.mu.Lock()
		call, running := rc.state.once[id]
		if !running {
			call = &onceCall{done: make(chan struct{})}
			rc.state.once[id] = call
			rc.state.mu.Unlock()
			return runOnce(rc, id, call, fetch)
		}
		rc.state.mu.Unlock()

		select {
		case <-call.done:
		case <-rc.Context().Done():
			return zero, rc.Context().Err()
		}

		if call.abandoned {
			// The caller that fetched was cancelled, which says nothing about the
			// key: ask again, as the first caller now.
			continue
		}
		if call.err != nil {
			return zero, call.err
		}
		// The entry is keyed by T too, so its value was a T. The assertion fails
		// only for a nil under an interface T, whose zero is that nil.
		value, _ := call.value.(T)
		return value, nil
	}
}

// runOnce is the first caller's half of Once: it fetches, publishes the result to
// the callers waiting on call, and returns it.
//
// Publishing is deferred so that it happens however the fetch ends. A fetch that
// panics used to leave call.done open, and every caller waiting for the key sat
// out its whole timeout for a result that was never coming; now they are released
// with the panic as an error, and the panic goes on to the caller that ran it.
//
// A fetch that failed because this caller's own context ended is not a result.
// The fetch runs under the first caller's context, and a prefetch whose slot the
// template never rendered is cancelled on purpose; sharing that cancellation
// failed a sibling that still wanted the value. The entry is withdrawn and the
// waiters, told so, fetch for themselves.
func runOnce[T any](rc *RenderContext, id keyID, call *onceCall, fetch func(context.Context) (T, error)) (T, error) {
	var zero T
	defer func() {
		if recovered := recover(); recovered != nil {
			call.err = fmt.Errorf("collage: Once %q: the fetch panicked: %v", id.name, recovered)
			publish(rc, id, call)
			panic(recovered)
		}
		if ctxErr := rc.Context().Err(); ctxErr != nil && errors.Is(call.err, ctxErr) {
			call.abandoned = true
		}
		publish(rc, id, call)
	}()

	if fetch != nil {
		value, err := fetch(rc.Context())
		call.value, call.err = value, err
	}
	if call.err != nil {
		return zero, call.err
	}
	value, _ := call.value.(T)
	return value, nil
}

// publish wakes the callers waiting on call, first withdrawing it from the table
// when it is to be asked again.
func publish(rc *RenderContext, id keyID, call *onceCall) {
	if call.abandoned {
		rc.state.mu.Lock()
		if rc.state.once[id] == call {
			delete(rc.state.once, id)
		}
		rc.state.mu.Unlock()
	}
	close(call.done)
}

package render

import (
	"context"
	"errors"
	"time"

	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/types"
)

// DocumentResult is one document execution's output. Like Result, it is non-nil on
// every path including a failure, so a caller can always read Timing and NotFound.
// Check the error before reading Body.
type DocumentResult struct {
	// Body is the bytes to serve. It is nil whenever ExecuteDocument returned an
	// error.
	Body []byte
	// ContentType is copied from the document, for the caller to write verbatim.
	ContentType string
	// Tags are the dependency tags this response was derived from, deduplicated,
	// sorted and with empty entries dropped.
	Tags []string
	// NotFound reports that the handler failed with an error wrapping
	// types.ErrNotFound, meaning the response is a 404 rather than a 500. It is a
	// classification of the failure, not a success.
	NotFound bool
	// Timing records how long the handler took.
	Timing observability.Timing
}

// ExecuteDocument runs doc's handler with panic containment and the engine's
// default timeout — Document has no per-document Timeout field, unlike Fragment, so
// there is no effective timeout to resolve — and returns its body, content type and
// collected tags. The handler runs on the calling goroutine — see Execute for why a
// spawned one would leak, and for the limitation that a handler ignoring its context
// can still overrun.
func (e *SlotEngine) ExecuteDocument(ctx context.Context, doc *types.Document, rc *types.RenderContext) (*DocumentResult, error) {
	started := time.Now()
	result := &DocumentResult{ContentType: doc.ContentType}

	// The same asset resolver and data cache a page's handlers have: a sitemap or
	// a feed reads the same records the pages do, and Cached in its handler was
	// otherwise Once.
	e.bindAssets(rc)

	var (
		body []byte
		tags []string
	)
	err := Execute(ctx, e.defaultTimeout, func(ctx context.Context) error {
		var handlerErr error
		body, tags, handlerErr = doc.Handler(ctx, rc.WithContext(ctx))
		return handlerErr
	})

	result.Timing.Total = time.Since(started)
	result.Timing.Data = result.Timing.Total
	e.metrics.RenderDuration(ctx, doc.Name, result.Timing.Total, false)

	// Tags first, error second: a handler that resolved what it depends on and
	// then failed has still told us what would invalidate this response — the same
	// rationale attempt() in fragment.go applies to a data handler's tags.
	result.Tags = sortedTags(append(append(append([]string(nil), tags...), doc.DependencyTags...), types.DeclaredTags(rc)...))

	if err != nil {
		result.NotFound = errors.Is(err, types.ErrNotFound)
		return result, err
	}

	result.Body = body
	return result, nil
}

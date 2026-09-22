package observability

import "context"

// Tracer starts spans around framework operations. Implementations must be safe for
// concurrent use and must not block, for the same reason as Metrics: the framework
// calls StartSpan on the request path.
type Tracer interface {
	// StartSpan starts a new span named name as a child of any span already in
	// ctx, returning a context carrying the new span and the Span itself. The
	// caller must call Span.End when the operation the span covers finishes.
	StartSpan(ctx context.Context, name string) (context.Context, Span)
}

// Span is a single traced operation, as started by Tracer.StartSpan.
type Span interface {
	// SetAttribute attaches a key/value pair to the span.
	SetAttribute(key, value string)
	// RecordError attaches err to the span as a failure. Calling it with a nil
	// err is a no-op.
	RecordError(err error)
	// End marks the span as finished. A Span must not be used after End is
	// called.
	End()
}

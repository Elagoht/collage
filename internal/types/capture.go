package types

import (
	"context"
	"net/http"
)

// CapturedResponse is what the application answers a static file's path with,
// as a static host can carry it. The static build asks for every file it wrote
// and hands the result to BuildFinishedHook on each BuiltFile.
type CapturedResponse struct {
	// Status is the status the application answered with.
	Status int
	// OtherStatus is the second response's status when it differed from
	// Status, and 0 when the two agreed.
	OtherStatus int
	// Headers are the stable, host-carriable response headers, canonicalised,
	// values in the order the application set them.
	Headers http.Header
	// Unstable names, sorted, the headers that differed between two requests
	// for the path — a nonce, a timestamp — and were left out of Headers.
	Unstable []string
	// Err is why the path could not be captured — the application did not
	// answer it in time, say — and nil when it was. Nothing else is set then.
	Err error
}

// CaptureKey is the context key WithCapture sets. It is exported so a context
// that hides request values from a shared render can still let it through:
// whether a request is a build's capture is about the request, not a reader.
type CaptureKey struct{}

// WithCapture returns ctx marked as a static build's header capture.
func WithCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, CaptureKey{}, true)
}

// IsCapture reports whether ctx is a request a static build sent to record a
// file's response headers.
func IsCapture(ctx context.Context) bool {
	marked, _ := ctx.Value(CaptureKey{}).(bool)
	return marked
}

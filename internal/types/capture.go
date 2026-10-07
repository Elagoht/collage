package types

import "net/http"

// CapturedResponse is what the application answers a static file's path with,
// as a static host can carry it. The static build asks for every file it wrote
// and hands the result to BuildFinishedHook on each BuiltFile.
type CapturedResponse struct {
	// Status is the status the application answered with.
	Status int
	// Headers are the stable, host-carriable response headers, canonicalised,
	// values in the order the application set them.
	Headers http.Header
	// Unstable names, sorted, the headers that differed between two requests
	// for the path — a nonce, a timestamp — and were left out of Headers.
	Unstable []string
}

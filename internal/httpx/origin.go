package httpx

import "context"

// OriginFunc returns the public origin URLs for host are absolute against.
type OriginFunc func(ctx context.Context, host string) string

type originsKey struct{}

// WithOrigins returns ctx carrying fn, for BaseURL. It is one of the framework's
// own keys, kept in a shared render's context: the origin is a function of the
// host, and the host is in the cache key.
func WithOrigins(ctx context.Context, fn OriginFunc) context.Context {
	return context.WithValue(ctx, originsKey{}, fn)
}

// BaseURL returns host's origin through the OriginFunc ctx carries, or "" when it
// carries none.
func BaseURL(ctx context.Context, host string) string {
	fn, ok := ctx.Value(originsKey{}).(OriginFunc)
	if !ok || fn == nil {
		return ""
	}
	return fn(ctx, host)
}

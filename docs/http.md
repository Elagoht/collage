# Middleware, your own handlers, and `Vary`

Collage renders pages. Everything else a Go program does over HTTP — an API, auth,
language negotiation, rate limiting — you write the way you already would, and plug
in at one of two points.

## `app.Use`: middleware

```go
app.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ...
		next.ServeHTTP(w, r)
	})
})
```

The standard `func(http.Handler) http.Handler` shape, so any middleware written for
`net/http` works. The first registered is the outermost.

It runs before routing, so it sees every request — pages, documents, actions,
mounts and handlers — and it runs *inside* the framework's span, metrics and panic
guard. That is the difference from wrapping `app.Handler()` yourself: a panic in
your middleware is a 500 on the normal error path rather than a dropped connection,
and a request it answers by itself — a 401, a redirect — is counted like any other.

Whatever it puts in the request's context is what data handlers read:

```go
type langKey struct{}

func pageData(ctx context.Context, rc *collage.RenderContext) (view, []string, error) {
	lang, _ := ctx.Value(langKey{}).(string)
	// ...
}
```

A static build renders without a request, so no middleware runs for it.

## `collage.Vary`: content that depends on the request

A cached page whose content depends on a request header serves the first reader's
version to everyone, because the cache key is the URL. `collage.Vary` tells the
cache:

```go
app.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := "en"
		if strings.HasPrefix(r.Header.Get("Accept-Language"), "tr") {
			lang = "tr"
		}
		collage.Vary(r, "Accept-Language", lang)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), langKey{}, lang)))
	})
})
```

- **The value you resolved enters the key, not the raw header.** `tr-TR,tr;q=0.9`
  and `tr` are one entry, because they are one page; a browser's every spelling of
  its preferences is not a new cache entry.
- **The header name goes into the response's `Vary` header**, so a CDN or proxy
  between you and the reader keeps the versions apart too.
- **Call it from middleware.** The key is computed before the page renders, so
  `Vary` from a data handler returns `collage.ErrVaryTooLate` rather than
  pretending to work.
- A query string cannot spell a declared dimension: the two are separate parts of
  the key.

This is how to negotiate a language without collage doing it for you — see
[routing](routing.md#locales) for why it no longer does.

## `app.Handle`: your own handler

```go
api := chi.NewRouter() // or http.NewServeMux, echo, a gRPC gateway, anything
api.Get("/api/users/{id}", getUser)

app.Handle("/api/", api)
```

Every request whose path begins with the prefix goes to your handler, with the
path unchanged — wrap it in `http.StripPrefix` if it expects otherwise.

Collage does nothing to it. **No forgery check, no body limit, no cache**: what the
handler accepts is yours to decide, and yours to get right. What it does get is
what every request gets — the span, the metrics, the panic guard, the middleware,
and draining on shutdown. A 5xx it answers with is reported to the error hooks; its
4xx answers are its own business.

The prefix must begin and end with `/` and cannot be `/` alone. A prefix that
covers a page, a document, an action, a redirect or a mount is refused at startup,
whichever was registered first — `app.Handle("/api/", …)` next to an action at
`/api/count` is `ErrMountShadowsRoute`.

A handler can still invalidate pages: `app.InvalidateTags(ctx, "post:42")` from
inside it drops what that change made stale.

For an endpoint that should get the forgery check and the body limit — a form
post, a small JSON endpoint next to the pages it changes — use an
[action](actions.md) instead.

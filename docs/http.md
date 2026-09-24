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

## `collage.SkipCache`: previews

An editor looking at an unpublished draft must see the draft, not the published page
the cache holds — and the draft must never become the page everyone else is served.
`collage.SkipCache(r)`, from middleware, gives a request a fresh render that is
neither read from the cache nor written to it, and marks the response `private,
no-store`.

Who may preview is yours to decide; collage has no opinion about sessions. A signed
cookie is enough for most sites. An action turns preview on when the CMS sends an
editor to it with a secret:

```go
var previewKey = []byte(os.Getenv("PREVIEW_KEY"))

func sign(value string) string {
	mac := hmac.New(sha256.New, previewKey)
	mac.Write([]byte(value))
	return value + "." + hex.EncodeToString(mac.Sum(nil))
}

// GET /api/preview?secret=...&slug=... — the URL the CMS's "preview" button opens.
func startPreview(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
	query := rc.Request.URL.Query()
	if !hmac.Equal([]byte(query.Get("secret")), []byte(os.Getenv("PREVIEW_SECRET"))) {
		return collage.NoContent(http.StatusUnauthorized), nil
	}
	target, err := app.URL("blog-post", "", map[string]string{"slug": query.Get("slug")})
	if err != nil {
		return nil, err
	}
	result := collage.SeeOther(target)
	result.Header = http.Header{"Set-Cookie": {(&http.Cookie{
		Name: "preview", Value: sign("on"), Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 3600,
	}).String()}}
	return result, nil
}
```

and middleware honours it:

```go
type draftsKey struct{}

app.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("preview"); err == nil && hmac.Equal([]byte(cookie.Value), []byte(sign("on"))) {
			collage.SkipCache(r)
			r = r.WithContext(context.WithValue(r.Context(), draftsKey{}, true))
		}
		next.ServeHTTP(w, r)
	})
})
```

A data handler asks the CMS for drafts when `ctx.Value(draftsKey{})` is true. A static
export renders without a request, so it never sees a draft.

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

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

A plugin's middleware counts as registered where the plugin was. The plugins in
`Config.Plugins` are registered in `collage.New`, so their middleware is outside
everything `app.Use` adds, and your middleware reads what they put in the
context — the signed-in reader's session, for one:

```go
app.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := session.FromContext(r.Context()).Get(session.UserKey)
		user, err := users.Active(r.Context(), id) // nil for a disabled account
		if err != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r.WithContext(withUser(r.Context(), user)))
	})
})
```

A plugin that should read what *your* middleware put there — a rate limit keyed
on that user — is registered after it, with `app.RegisterPlugin`, and its
middleware goes where the call is made: inside what `app.Use` added before it,
outside what it adds after.

```go
app.Use(loadUser)
app.RegisterPlugin(ratelimit.New(ratelimit.Options{KeyFunc: userID}))
```

`RegisterPlugin` refuses a plugin that adds template functions, which has to be in
`Config.Plugins`.

It runs before routing, so it sees every request — pages, documents, actions,
mounts, a plugin's handlers and a handler mounted `WithMiddleware` — except one for
a handler mounted with plain `app.Handle`, which is answered without it (see
[below](#apphandle-your-own-handler)). It runs *inside* the framework's span,
metrics and panic guard. That is the difference from wrapping `app.Handler()` yourself: a panic in
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
		return collage.Status(http.StatusUnauthorized), nil
	}
	target, err := rc.URL("blog-post", map[string]string{"slug": query.Get("slug")})
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
what happens to every request before it is routed — the plugins' request hooks (an
incoming trace), the span, the metrics, the panic guard, the clean-path redirect
and encoded-slash 404 [below](#paths-are-cleaned-first), the baseline
`X-Content-Type-Options` and `X-Frame-Options` headers — and draining on shutdown.
A 5xx it answers with is reported to the error hooks; its 4xx answers are its own
business.

### No middleware, unless you ask

Since v0.67.0 **no middleware runs for it**: neither what `app.Use` added nor any
plugin's — session, flash, i18n, consent, secure, ratelimit, basicauth, compress,
fail2ban, accesslog, redirects, tenant, the development toolbar. Your handler, your
middleware: an API has its own idea of
auth (a bearer token, not the site's session cookie), its own rate limit, its own
error format, and the site's consent banner, HTML toolbar or `Vary: Cookie` are
nothing it asked for. So wrap the handler in what it wants, the way you would
without collage:

```go
api := http.NewServeMux()
api.HandleFunc("GET /api/users/{id}", getUser)

// requireToken is yours, below; perClientLimit stands for any net/http rate
// limiter — golang.org/x/time/rate behind a map, httprate, tollbooth.
app.Handle("/api/", requireToken(perClientLimit(api)))

func requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validToken(r.Header.Get("Authorization")) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

A handler that does rely on the site's middleware — a `basicauth` plugin you
registered to guard it, the reader's session, a rate limit you already configured —
asks for it back with `collage.WithMiddleware()`, and then runs inside the chain
exactly as a page does, in the same order:

```go
app.Handle("/admin/api/", adminAPI, collage.WithMiddleware())
```

**No plugin can answer for a bare mount: a ban, an IP allowlist, a maintenance
page or an access log over the whole site does not cover it. Mount it
`WithMiddleware()` for that, or put your own middleware around your mux.** For
one, fail2ban still counts a bare mount's probes — its `RequestHook` runs for every
request — but the 403 it answers a banned client with is middleware, so that client
keeps full access to `/api/`, which is where login brute force usually happens.
Likewise accesslog does not log a bare mount's traffic, a redirects rule under its
prefix does not fire, and tenant's context is not in its requests.

Which requests skip the middleware is decided on the path as it is after the
cleaning [below](#paths-are-cleaned-first), by the same match routing uses: a
prefix ending in `/` claims what begins with it, one without its exact path alone
(`/api` is not `/apix`, nor `/api/`). Since no page, document, action or mount may
share a handler's prefix, a request that skips the middleware can only reach that
handler. One consequence: a middleware that rewrites a request's path *into* a
plain `Handle` prefix no longer reaches it — the request comes out of the chain,
and is routed as everything else the chain hands on, to a 404. Mount the target
`WithMiddleware` if you rewrite into it. The other way round, a middleware that
rewrites a path *out of* a plain `Handle` prefix — `/api/legacy` to a page — no
longer runs for it at all: the mount claims the request first, before any
middleware. Mount it `WithMiddleware` if a middleware should see its requests.

A plugin's own endpoints — a live-reload stream, an OAuth callback, `/metrics`
from a Prometheus plugin, an upload endpoint — are mounted with `Host.Handle`, and
those always run inside the middleware: they are part of the site, and may read
the session or rely on a `basicauth` in front of them.

The prefix must begin and end with `/` and cannot be `/` alone. A prefix that
covers a page, a document, an action, a redirect or a mount is refused at startup,
whichever was registered first — `app.Handle("/api/", …)` next to an action at
`/api/count` is `ErrMountShadowsRoute`.

A handler can still invalidate pages: `app.InvalidateTags(ctx, "post:42")` from
inside it drops what that change made stale.

For an endpoint that should get the forgery check and the body limit — a form
post, a small JSON endpoint next to the pages it changes — use an
[action](actions.md) instead.

## Paths are cleaned first

A path with dot segments or doubled slashes — `/a/../b`, `/a//b`, `/a/./b` — is
redirected to its clean spelling before anything reads it, middleware, plugins and
your own handlers included: 301 for GET and HEAD, 308 for a method that carries a body, the query
kept. A middleware that skips `/_collage/` or protects `/admin/` therefore never
meets `/_collage/../admin`.

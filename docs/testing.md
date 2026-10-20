# Testing

A collage application is an `http.Handler`: `app.Handler()` returns it, with no
server listening and no port to choose. `pkg/collagetest` drives that handler from a
Go test the way a browser does, so a test exercises routing, data handlers,
templates, the cache, forms, plugins and middleware exactly as they run in
production.

```go
import "github.com/Elagoht/collage/pkg/collagetest"

func TestLogin(t *testing.T) {
	app, err := newApp(collage.Env{CacheDir: t.TempDir()}) // the function main hands collage.Main
	if err != nil {
		t.Fatal(err)
	}
	c := collagetest.New(t, app.Handler())

	page := c.Get("/login").WantStatus(http.StatusOK)
	res := c.Submit(page, "/login", url.Values{
		"email":    {"ada@example.com"},
		"password": {"correct horse"},
	}).WantStatus(http.StatusSeeOther)

	if res.Location() != "/panel" {
		t.Errorf("Location = %q, want /panel", res.Location())
	}
	panel := c.Follow(res).WantStatus(http.StatusOK)
	// panel.Body is the page the cookie set by the login opened
}
```

## The client

| | |
| --- | --- |
| `collagetest.New(t, h)` | A client for `h` with an empty cookie jar. Two readers are two clients. |
| `c.Get(target)` | A `GET` of a path or an absolute URL. |
| `c.Submit(page, action, values)` | Submits the form of `page` whose action is `action`. |
| `c.Follow(res)` | A `GET` of the `Location` a redirect names. |
| `c.Request(method, target, body)` / `c.Do(req)` | Any other request — a JSON body, a header of its own. `Do` attaches the jar's cookies and keeps the ones the response sets. |

A `*Response` carries `Status`, `Header`, `Body`, `URL` and `Method`.
`WantStatus(code)` fails the test with the body when the status differs, and returns
the response, so a request and its check read as one line. `Location()` is the
`Location` header. `CSRFToken()` is the value of the page's first hidden `_csrf`
input, for a request that sends the token in the `X-CSRF-Token` header as a
`fetch()` does:

```go
req := c.Request(http.MethodPost, "/api/count", nil)
req.Header.Set("X-CSRF-Token", page.CSRFToken())
c.Do(req).WantStatus(http.StatusOK)
```

## What Submit sends

`Submit` finds the form, carries across **every hidden input** it holds — the
forgery token `{{csrfToken}}` rendered, and whatever a plugin stamps into a form,
such as a honeypot's signed timestamp — and puts `values` on top: a name in `values`
replaces a hidden input of the same name. Visible fields are the test's to fill; a
trap field a bot would fill stays empty, so the submission is a reader's.

- `action` is resolved against the page's URL and compared decoded, as is the
  form's: `"/login"`, `"login"` from a page beside it, an absolute URL, and
  `"/giriş"` for a form whose action is `"/giri%c5%9f"` all name the same form. A
  form with no `action` submits to its page. An empty `action` means the page's
  only form. No match, or several, fails the test and lists the actions the page's
  forms have.
- The form's `method` and `enctype` are honoured: a `GET` form sends its values in
  the query, `multipart/form-data` is sent as multipart, anything else as
  `application/x-www-form-urlencoded`.
- Forms are found by a scanner, not an HTML parser: comments and the bodies of
  `<script>`, `<style>`, `<template>` and `<textarea>` are skipped, so markup
  written out as text there is not taken for a form. A disabled hidden input is
  not sent, as a browser does not send it.

## Cookies and redirects

The client keeps cookies in a `net/http/cookiejar`, scoped by path and expiry as a
browser scopes them. A path alone is addressed to `http://example.com`, the host
`net/http/httptest` uses. A site that marks its cookies `Secure` uses absolute
`https://example.com/...` targets, which `httptest` marks as arriving over TLS, so
the jar sends those cookies back.

Redirects are not followed: a test usually wants to see the `303` and where it
points. `Follow` takes it when the page behind it is what the test is about.

## Invalidation and warming

After an `InvalidateTags` or `PurgeTags`, the application re-renders the pages it
reached in the background. A test that asserts `X-Collage-Cache: STALE` (or a
`MISS`) right after an invalidation races that warmer; set `WarmLimit: -1` in
`CacheConfig` for such a test. Warming is already off in development.

A test that asserts the opposite, a `HIT` once the warmer has re-rendered the
page, waits for it with `site.WaitWarmed()`, which returns once nothing is
queued or rendering, and at once when warming is off:

```go
if err := site.App.InvalidateTags(ctx, "posts"); err != nil {
	t.Fatal(err)
}
site.WaitWarmed()
res := site.Get("/posts").WantStatus(http.StatusOK)
// res.Header.Get(collage.CacheStatusHeader) == "HIT"
```

## Test the application main builds

The scaffolded `main.go` builds the application in `newApp`, which `main` calls to
serve and the tests call to test, so the tests exercise the site that runs rather
than a second wiring. Point the disk cache at `t.TempDir()` with the `Env` you
pass it, `collage.Env{CacheDir: t.TempDir()}` —
otherwise a test can be served a page an earlier test or run rendered — and shut
the application down in a `t.Cleanup` registered after it: the data cache writes
to that directory in the background, and `App.Shutdown` waits for those writes,
so the directory is not removed while one is still being made. The demo project
`collage new --template demo` scaffolds has a `main_test.go` written this way.

## Test a plugin with a site of its own

A plugin has no application of its own to build. `collagetest.NewSite(t, ...)`
builds a real one around it — a layout, a home page at `/`, a captured debug log —
starts it, shuts it down when the test ends, and gives back the app, a `Client` on
it and its `Logs`. See [plugins.md](plugins.md#testing-a-plugin).

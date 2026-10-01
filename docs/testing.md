# Testing

A collage application is an `http.Handler`: `app.Handler()` returns it, with no
server listening and no port to choose. `pkg/collagetest` drives that handler from a
Go test the way a browser does, so a test exercises routing, data handlers,
templates, the cache, forms, plugins and middleware exactly as they run in
production.

```go
import "github.com/Elagoht/collage/pkg/collagetest"

func TestLogin(t *testing.T) {
	c := collagetest.New(t, newApp(t).Handler())

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

## Test the application main builds

The scaffolded `main.go` builds the application in `newApp`, which `main` calls to
serve and the tests call to test, so the tests exercise the site that runs rather
than a second wiring. Point the disk cache at `t.TempDir()` before building it —
otherwise a test can be served a page an earlier test or run rendered. The demo
project `collage new --template demo` scaffolds has a `main_test.go` written this
way.

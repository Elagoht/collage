# Actions, forms, and fragment URLs

A page answers `GET` and `HEAD`. Everything else — a form post, a `DELETE` from a
`fetch()`, a webhook — is an **action**.

```go
collage.NewPage("new-post").
	WithLayouts(layout).
	WithContent(form).
	WithPath("en", "/posts/new").
	WithAction("POST", createPost)
```

That is the ordinary case: the form's `action` is the page it sits on, so the POST
arrives at the page's own URL, and the action inherits that URL in every locale the
page declares. An action that wants a URL of its own is registered on its own
instead:

```go
app.RegisterAction(collage.NewAction("stripe-webhook").
	WithPath("en", "/hooks/stripe").
	WithMethods("POST").
	WithoutCSRF().
	WithHandler(onStripe).
	Build())
```

### Linking an action

A form posting to an action on its own URL names it with `actionURL` rather than
writing the path, so moving the action cannot leave a form posting to the old one:

```html
<form method="post" action="{{actionURL "logout"}}">{{csrfToken}}…</form>
<form method="post" action="{{actionURL "vote" "id" .ID}}">{{csrfToken}}…</form>
```

It takes parameters as `pageURL` does, uses the render's locale, and falls back to
the default one when the action has no path in it. A page's own action is named as
it was registered: `"new-post:POST"` for the `WithAction` above. From Go it is
`app.ActionURL(name, locale, params)`. An unknown name is `collage.ErrUnknownRoute`.
Actions have names of their own, apart from pages, because a `login` page and a
`login` action are the usual pair.

## A method nothing answers is a 405

A `POST` to a page with no action used to render the page. It is now a 405 carrying
an `Allow` header. The old behaviour was wrong in the quietest possible way: the
reader was handed a page that looks like nothing happened, and the application never
saw the submission.

`OPTIONS` is answered from the same list, so what a 405 offers and what an `OPTIONS`
reports cannot disagree.

`OPTIONS`, `TRACE` and `CONNECT` belong to the server, not to an action, and
registering an action that declares one fails with
`collage.ErrInvalidActionMethod`, naming the action and the method. The router
answers `OPTIONS` itself, and the forgery check counts it as a safe method like
`GET`, so an action claiming it would change state with no token checked. A CORS
preflight is a middleware's job, answered before the router.

## What a handler answers with

```go
func createPost(ctx context.Context, rc *collage.RenderContext) (*collage.ActionResult, error)
```

`ActionResult` has four ways to produce a body, checked in this order.

**`Location`** redirects, with 303 unless you say otherwise. 303 rather than 302 is
the point of the redirect: it turns the follow-up into a `GET`, so reloading the
destination does not submit the form again.

```go
return collage.SeeOther("/posts/" + slug), nil
```

Or by name, so a renamed path cannot leave the redirect pointing at the old one.
`rc.URL` builds in the request's locale and keeps a value inside its path
segment; see [Links by name](fragments.md#links-by-name):

```go
target, err := rc.URL("post", map[string]string{"slug": slug})
if err != nil {
	return nil, err
}
return collage.SeeOther(target), nil
```

**`Fragment`** answers with one fragment's markup — the changed part, not the page.

**`Page`** answers with a whole page, which is the shape a validation failure takes.
The handler and the render share one `RenderContext`, so what the handler learned is
there to be read:

```go
var formErrorKey = collage.NewKey[string]("error")

formErrorKey.Set(rc, "a title is required")
return &collage.ActionResult{Status: http.StatusUnprocessableEntity, Page: rc.Page}, nil
```

The page's data handler reads it back with `formErrorKey.Get(rc)`, as the type the
key declares.

`rc.Page` is the page whose URL the action answers on — the page the form was posted
from. The handler does not need to be handed its page, so it needs no variable
declared before the page and filled in after it. An action at a URL of its own has
no page there, and its `rc.Page` is `nil`: it answers with the registered page
itself. In dev mode a form post answered 422 with no body is logged as a warning,
because to the reader it is a blank page, and a `nil` `rc.Page` is the usual cause.

No session, no flash storage, no state smuggled through a query string: the handler
and the render are one request. The page is rendered as a page — `OnBeforeRender` and
`OnAfterRender` run for it, so whatever plugins do to a page, a minifier or a
structured-data plugin, they do to the one a validation failure answers with.

A page named in `Page` has to be **the value you registered** — which `rc.Page` always is. Registration is what puts a page's
content into its layout, so a page built inside the handler renders as a layout
around nothing; collage refuses it with `ErrUnregisteredPage`, naming the page, rather
than serve a blank one.

**`Body`** with a `ContentType` is written verbatim, which is what a webhook or a
JSON endpoint answers with. An empty `ContentType` is `application/octet-stream`,
never sniffed: guessing a type from bytes is how a text response becomes a download.
`collage.JSONOf(status, v)` marshals `v` and returns the result, so a JSON endpoint
is one line:

```go
return collage.JSONOf(http.StatusOK, countResponse{Count: n})
```

A result that sets none of them is a bare status, and a `nil` result is a 204.

An action's response is never cached, whatever the page it rendered was declared as.
It was produced from one submission and belongs to whoever sent it.

## Redirect or render? Failure renders, success redirects

Both work. `Page` is not only for failures — a successful submission can perfectly
well answer with the page it was sent from, carrying "thanks, we have your message":

```go
var sentKey = collage.NewKey[bool]("sent")

sentKey.Set(rc, true)
return collage.RenderPage(rc.Page), nil
```

What decides between them is not the framework but the browser. Answering a POST with
a page leaves the address bar on the URL that was posted to, and the history entry a
POST — so reloading submits the form again, behind a dialog most people click
through. Answering with a 303 makes the browser's next request a `GET` of somewhere
else, and reloading *that* is free.

Which gives a rule that is about what the two outcomes mean rather than about
mechanics:

**A refused submission renders the page.** The reader is going to fix it and send it
again, so resubmission is the intended next step rather than a hazard — and rendering
is what puts the reason and what they typed back in front of them. Answer 422, not
200: the request was understood and not acted on.

**An accepted submission redirects.** The work is done, and a reload must not do it
twice. Send the reader somewhere that can say so:

```go
return collage.SeeOther("/contact/thanks"), nil
```

A scaffolded project's `/hello` form is the second kind: its action sets what was
submitted and renders the page as the response.

The exception is a submission that changed nothing and can be repeated harmlessly: a
search, a filter, a preview. Those are usually a `GET` anyway, and a `Fragment` is a
better answer than either.

A form a script submits with `fetch` would follow the redirect too, downloading the
page it leads to, and then navigate there and have it rendered a second time. A
request carrying the header `Collage-Fetch` (`collage.FetchHeader`) is answered
`204 No Content` with the destination in `Collage-Location`
(`collage.LocationHeader`) instead of a redirect, and the script navigates once.
collage-live's forms send it.

## Invalidating what the change made wrong

```go
return &collage.ActionResult{
	Location:       "/posts",
	InvalidateTags: []string{"posts"},
}, nil
```

Declarative, and it runs **before** the response. An action that changed something
and then redirects must not have the reader follow that redirect into a cache entry
the change already made wrong.

## Request bodies are bounded

Four megabytes by default, `Server.MaxBodyBytes` for the application, `MaxBodyBytes`
for one action, and a negative value for unbounded. A body past the limit is a 413 —
with forgery protection on as well, where reading the token means reading the body:
an oversized form is refused for its size, not reported as a forged one.

A size rule of the action's own — a photo over 5 MB refused with a message on the
form — is reached only under a limit above it: below, collage's 413 answers first
and the message is never shown. Give the action room for the file and the rest of
the form, `WithMaxBodyBytes(maxPhoto + 64<<10)`.

A limit every handler has to remember is a limit the one handler that forgot does not
have, and that handler is the one an anonymous caller will find.

A handler that reads the body itself and runs into the limit is answered with `413`
too, whatever error it returns — the read's own, or one of its own such as "upload
failed". That error is kept beside the 413, in the log and in what error hooks
are handed. An action that reads a large body as a stream is in
[Streaming bodies](#streaming-bodies).

## Forgery protection

Every unsafe request to an action is checked. Put the token in the form:

```html
<form method="post" action="/posts/new">
  {{csrfToken}}
  <input type="text" name="title">
  <button type="submit">Publish</button>
</form>
```

`{{csrfToken}}` renders the whole hidden input, field name and all — a bare value has
to go in a field with exactly the right name, and a form that names it wrong fails in
a way that looks like the token is broken. `Security.CSRFFieldName` renames the field
in both places at once — the input `{{csrfToken}}` renders and the one the verifier
reads. A `fetch()` with no form to put a field in
sends the same value in `X-CSRF-Token`. One that posts a form —
`fetch(url, {method: "POST", body: new FormData(form)})` — sends it as
`multipart/form-data`, which is read like a URL-encoded body, so the hidden input is
enough.

A fragment or page an action answers with is rendered like any other, so a form in
it carries its token too: a form that replaces itself keeps working on the next
submission.

The scheme is a signed double-submit cookie: a token is a random nonce and its HMAC
under `Security.CSRFKey`, and the same value goes in a cookie and in the form.
Verifying needs the key and nothing else — no session table, no store to configure,
nothing shared between instances.

A signed token is not a secret from an attacker, though: any page with a form hands
one to whoever loads it. Someone who can write a cookie for the domain — a sibling
subdomain, a man-in-the-middle on plain http — could plant their own token in a
visitor's browser and submit it. So the pair is not checked alone. The browser says
where a request came from, in `Sec-Fetch-Site` (or `Origin`, compared with `Host`,
from an older browser), and a request it marks as coming from another origin is
refused with `403` whatever it carries — a sibling subdomain included. A request
with neither header is not from a browser, and passes to the token check. An
admin panel on its own origin that posts here is named in
`Security.CSRFTrustedOrigins`, each as `scheme://host[:port]` — case and a default
port do not matter, and a wildcard is refused: name each origin. The token cookie is
`SameSite=Lax`, so a trusted origin on another *site* never sends it; trusting one
is for another origin of the same site, such as a subdomain.

The cookie is `Secure` when the request arrived over TLS, or when the proxy in
front says it did in `X-Forwarded-Proto`. With `Server.TrustedProxies` set, that
header is believed only from a listed proxy: a client reaching the server directly
cannot claim TLS it does not have. Empty, it is believed from anyone, which is right
for a server only a proxy can reach. See
[Behind a proxy](deployment.md#behind-a-proxy-trustedproxies).

A multipart body over the 32 MiB the parser holds in memory spills its files to
disk; they are removed when the action has answered, accepted or refused.

**Set `Security.CSRFKey`.** An empty one is generated and logged — as a warning,
except in development — which is right for a first run and wrong to deploy: a
generated key differs in every process, so a token issued before a restart is refused
after it.

**A page with a form is not exported.** A built site has no server to put a token in
it or to submit it to, so `collage export` skips such a page and says why. It is
still served, and still cached if its strategy says so.

**A page with a form is still cached.** What is stored is the body with a *marker*
where the token goes; what goes on the wire is that body with the reader's own token
substituted in, along with the cookie it is checked against, and `private, no-store`.
The expensive part — the render — is shared; the one per-reader string is not.
Without this, a newsletter form in a site's footer would turn caching off for the
whole site.

The marker is derived from the key, which is what makes both halves work: it is the
same in every process that shares the key, so a body cached by one is still
substitutable by the next, and it cannot be computed by anyone who does not have the
key, so it cannot be planted in content the application did not write. Changing the
key does not empty the cache: a stored body carrying the old key's marker is treated
as a miss and rendered again, and a page without a form is served as it was.

`WithoutCSRF()` is for requests that cannot carry a token — a payment provider's
webhook, an API called with a bearer token by something that is not a browser. On
anything a browser submits, it gives the protection away.

Safe methods are not checked. A `GET` action changes nothing by contract, and a token
on it would be a token in a URL, which is a token in a log file and in a `Referer`
header.

## Streaming bodies

An action that takes a large upload reads its body as a stream, and nothing may read
it first. `WithStreamingBody()` makes that a guarantee:

```go
collage.NewAction("upload").WithPath("en", "/upload").WithMethods(http.MethodPost).
	WithStreamingBody().
	WithMaxBodyBytes(2 << 30).
	WithHandler(upload)
```

- **The body arrives unread.** Nothing before the handler parses a form out of it:
  `rc.Request.Body` is the bytes as they were sent, and `rc.Request.Form` and
  `rc.Request.MultipartForm` are nil. The handler reads it — with
  `rc.Request.MultipartReader()`, say, one part at a time.
- **The token comes in the header, and only there.** Finding it in a form field
  would mean parsing the body, so the field is not looked for: a request without
  `X-CSRF-Token` (or `Security.CSRFHeaderName`) is refused with `403`, and the
  reason, `ErrCSRFHeaderRequired`, names the header. The reason goes to the log
  and to error hooks, and onto the built-in error page in development; the reader
  sees only the 403, and an error page of the application's own hides it in
  development too. Send it from the page's own
  token — the cookie is `HttpOnly`, so read the value from the input `{{csrfToken}}`
  renders (named `_csrf`, or `Security.CSRFFieldName`):

  ```js
  const token = form.querySelector('input[name="_csrf"]').value;
  await fetch(form.action, {
    method: "POST",
    headers: {"X-CSRF-Token": token},
    body: new FormData(form),
  });
  ```

- **A plain HTML form cannot post to it.** A browser submitting a form has no way
  to set a header, so its token is in a field nobody reads, and it is refused.
  Submit with `fetch()` as above. `WithoutCSRF()` still turns the check off
  entirely, and the body still arrives unread.
- **`MaxBodyBytes` still applies, and must be raised.** The four-megabyte default
  bounds a streaming body as it bounds any other, enforced as the handler reads.
  A handler that reads past it and fails is answered with `413`, whatever error it
  returns.
- **Anything that parses the form consumes the stream.** `rc.Request.FormValue`,
  `ParseMultipartForm`, a helper such as `validate.Form` — each reads the body to
  its end, and the handler finds nothing left. `BeforeActionEvent.Form()` will not
  do this: for such an action it returns `ErrStreamingBody` without reading a
  byte, and a plugin that inspects forms treats that error as "this action has no
  form to check" and lets the request through. A plugin that calls
  `ev.Request.ParseForm()` itself still consumes the stream.

Registration refuses `WithStreamingBody()` on an action answering none of `POST`,
`PUT` or `PATCH`, with `ErrStreamingBodyMethod`.

## A fragment at its own URL

```go
collage.NewPage("search").
	WithContent(searchContent).
	WithPath("en", "/search").
	WithFragmentPath("en", "/search/results", resultsFragment)
```

`GET /search/results?q=grid` renders that fragment and nothing else — its data
handler runs, its children are prefetched and rendered, its failure policy applies,
because it is the same walk started lower down. That includes the difference between
missing and broken: a required fragment whose data handler wraps `ErrNotFound` answers
404, any other failure 500 — as plain text, the way every action's failure does.

It is the answer to refreshing part of a page without a client framework: fetch the
URL, replace the element. Combined with an action that returns a `Fragment`, a form
can post and be answered with only what changed.

**Each fragment path is a render of its own.** Inside a page, fragments that need
the same slow value share it with `Once`: one fetch per render. A page refreshed
part by part is several renders, and `Once` shares nothing between them. For
fragments that read the same data, use `Cached`, which keeps the value across
renders for as long as its TTL — or until one of its tags is invalidated:

```go
var statsKey = collage.NewKey[monitor.Stats]("system:stats")

stats, err := collage.Cached(rc, statsKey, time.Second, nil,
	func(ctx context.Context) (monitor.Stats, error) { return monitor.Collect(ctx) })
```

A measurement like this one takes no tags: its TTL keeps it fresh, and each
fragment reading it returns a tag of its own. Tagged, every fragment would depend on
every tag, and invalidating one part would re-render them all.

**Nothing is reachable unless it is declared.** A framework that exposed every
fragment automatically would put every internal part of every page on the public web,
and turning that off again is not something anyone remembers to do.

A fragment path is claimed like any other route. One spelled like a page's path, a
document's, another page's fragment path, or a redirect's source is refused at
registration, in whichever order the two arrive: it would otherwise hide the page
without a word.

### Linking one

Write the path once, in `WithFragmentPath`, and build every link to it by name,
as `pageURL` does for pages:

```html
<div data-live="{{fragmentURL "home" "cpu-usage"}}">{{slot "cpu-usage"}}</div>
<div data-live="{{fragmentURL "post" "comments" "slug" .Slug}}">…</div>
```

`fragmentURL` uses the render's locale and falls back to the default one;
`fragmentURLIn "tr" "home" "cpu-usage"` names the locale. In Go it is
`app.FragmentURL("home", "cpu-usage", locale, params)`. It is as strict as
`App.URL`: an unknown page is `ErrUnknownRoute`, a fragment the page did not open is
`ErrUnknownFragmentPath`, a fragment opened at two paths in one locale is
`ErrAmbiguousFragmentPath`, and missing parameters are `ErrRouteParams` — in a
template, each fails the render.

### What it hoists

A fragment answered on its own has no layout around it. A marker the fragment
writes itself is filled as in a page; what it hoisted into any other area — a
stylesheet asked for with `{{stylesheet}}`, a title — comes ahead of the markup,
one inert `<template>` per item:

```html
<template data-collage-hoist="head" data-collage-key="stylesheet:/static/chart.css"><link rel="stylesheet" href="/static/chart.css"></template>
<section>…the fragment…</section>
```

The key is the one the page's own head deduplicated by, so a script can add to
`document.head` what it does not already have. A client that ignores the channel
inserts template elements, which render nothing and run nothing.

### Revalidation

The answer to a GET carries an `ETag`, the hash of the body as sent, and
`Cache-Control: private, no-cache`. A request with a matching `If-None-Match` is
answered `304` with no body. The render still runs; what is saved is the body on
the wire and the client's work, which for a panel refreshed every few seconds is
most of it. A handler that sets `Cache-Control` itself keeps its own, unless a
`PersonaliseHook` made the body personal (since v0.43.0): then it is
`private, no-cache` whatever the handler set.

The response is never cached by the framework.

### Refreshing it from the browser

The framework ships no client script. [collage-live](https://github.com/Elagoht/collage-live)
is a plugin that does: it refreshes elements marked with `data-collage-fragment` on
an interval or when the server pushes a change over an event stream, and applies
the hoist channel and the ETag above. The protocol it speaks is this page, so htmx
or a script of your own works against the same server.

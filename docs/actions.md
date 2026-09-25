# Actions, forms, and fragment URLs

A page answers `GET` and `HEAD`. Everything else — a form post, a `DELETE` from a
`fetch()`, a webhook — is an **action**.

```go
collage.NewPage("new-post").
	WithLayout(layout).
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

## A method nothing answers is a 405

A `POST` to a page with no action used to render the page. It is now a 405 carrying
an `Allow` header. The old behaviour was wrong in the quietest possible way: the
reader was handed a page that looks like nothing happened, and the application never
saw the submission.

`OPTIONS` is answered from the same list, so what a 405 offers and what an `OPTIONS`
reports cannot disagree.

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

**`Fragment`** answers with one fragment's markup — the changed part, not the page.

**`Page`** answers with a whole page, which is the shape a validation failure takes.
The handler and the render share one `RenderContext`, so what the handler learned is
there to be read:

```go
rc.Set("error", "a title is required")
result := collage.RenderPage(formPage)
result.Status = http.StatusUnprocessableEntity
return result, nil
```

No session, no flash storage, no state smuggled through a query string: the handler
and the render are one request. The page is rendered as a page — `OnBeforeRender` and
`OnAfterRender` run for it, so whatever plugins do to a page, a minifier or a
structured-data plugin, they do to the one a validation failure answers with.

`formPage` has to be **the value you registered**. Registration is what puts a page's
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
rc.Set("sent", true)
return collage.RenderPage(contactPage), nil
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

A limit every handler has to remember is a limit the one handler that forgot does not
have, and that handler is the one an anonymous caller will find.

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

## A fragment at its own URL

```go
collage.NewPage("search").
	WithContent(searchContent).
	WithPath("en", "/search").
	WithFragmentPath("en", "/search/results", resultsFragment)
```

`GET /search/results?q=grid` renders that fragment and nothing else — its data
handler runs, its children are prefetched and rendered, its failure policy applies,
because it is the same walk started lower down.

It is the answer to refreshing part of a page without a client framework: fetch the
URL, replace the element. Combined with an action that returns a `Fragment`, a form
can post and be answered with only what changed.

**Each fragment path is a render of its own.** Inside a page, fragments that need
the same slow value share it with `Once`: one fetch per render. A page refreshed
part by part is several renders, and `Once` shares nothing between them. For
fragments that read the same data, use `Cached`, which keeps the value across
renders for as long as its TTL — or until one of its tags is invalidated:

```go
stats, err := collage.Cached(rc, "system:stats", time.Second, []string{"system"},
	func(ctx context.Context) (monitor.Stats, error) { return monitor.Collect(ctx) })
```

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
most of it. A handler that sets `Cache-Control` itself keeps its own.

The response is never cached by the framework.

### Refreshing it from the browser

The framework ships no client script. [collage-live](https://github.com/Elagoht/collage-live)
is a plugin that does: it refreshes elements marked with `data-collage-fragment` on
an interval or when the server pushes a change over an event stream, and applies
the hoist channel and the ETag above. The protocol it speaks is this page, so htmx
or a script of your own works against the same server.

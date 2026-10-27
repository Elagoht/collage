package core

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Elagoht/collage/internal/ascii"
	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/internal/httpx"
	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/types"
)

// ErrNoRequest is returned by RenderFragment when it is given no request to render
// for.
var ErrNoRequest = errors.New("collage: no request to render for")

// RenderFragment renders one fragment a page opened at its own URL with
// WithFragmentPath, for r, and returns it in parts: the markup, what it hoisted,
// the tags it depended on, and whether one render can serve every reader.
//
// It is the render a request to the fragment's path gets, without the response: a
// plugin pushing fragments over an event stream or a WebSocket calls it for each
// fragment an invalidation touched, and sends the parts in a message of its own.
// Only a fragment the page opened is rendered — the same set of fragments HTTP
// reaches — so a stream cannot be used to read a part of a page nobody published.
// Values read through collage.Cached are fresh: the render waits for the
// upstream rather than be built from a stale value, and fails when it is down.
//
// Equivalent to that request, not merely like it, because r is usually not one
// for the fragment at all: it is the stream's, which the stream's own path let
// through. So the render is made for a GET to the fragment's URL, carrying r's
// host, client address, headers and cookies, and nothing else of r's — not its
// path values, its body, its method or its context's values (see
// fragmentContext) — and that request goes through what a request to the URL
// goes through:
//   - the application's and every plugin's middleware, in ServeHTTP's order, for
//     the fragment's path. One that answers instead of passing the request on —
//     a basicauth's 401, a rate limit's 429, a redirect to a login page — refuses
//     the reader: ErrGuardRefused, naming the status, and what it wrote goes
//     nowhere. What it puts in the context, the guard and the render read. The
//     headers and cookies it sets are dropped: the caller's response is its own.
//   - the route's parameters, on the request as r.PathValue, for the guards.
//   - the page's and the fragment's guards.
//   - the reader's forgery token, then every PersonaliseHook, on the markup and
//     on each item it hoisted. A render a hook made personal is not Shared.
//
// The request hooks are not run, nor the span and metrics: no request reached
// the server.
//
// A Shared render is kept, after this reader's chain and guards have passed, and
// handed to the next reader they pass: see fragmentMemo. The token and the hooks
// run on each reader's copy.
//
// It starts the application if nothing has yet, as Handler does, and refuses to
// render while a start is running — from a plugin's Init, say — since the
// middleware chain does not exist until it has finished.
//
// Plugins reach it through Host.RenderFragment.
func (a *App) RenderFragment(r *http.Request, req plugin.FragmentRequest) (*plugin.FragmentRender, error) {
	if r == nil {
		return nil, ErrNoRequest
	}
	handler, err := a.servingHandler()
	if err != nil {
		return nil, err
	}
	// A middleware rendering a fragment for the request it was handed — itself
	// a fragment's — passes through itself again, and again, until the stack
	// runs out. A few levels are someone's design; past them it is that loop.
	depth := fragmentDepth(r.Context()) + 1
	if depth > maxFragmentDepth {
		return nil, fmt.Errorf("%w: %d renders deep", errFragmentDepth, depth-1)
	}
	target, err := a.fragmentTarget(req)
	if err != nil {
		return nil, err
	}
	ctx, done := fragmentContext(r.Context(), depth)
	defer done()
	forFragment := fragmentRequest(r, target).WithContext(ctx)
	// Resolved before the chain as well as at its end: a URL that reaches no
	// fragment is refused without a middleware counting it — a rate limit, a
	// ban list — as a request the reader made.
	if _, err := a.fragmentAt(forFragment); err != nil {
		return nil, err
	}

	// Written by the render, which ServeFragment does not return before it has
	// finished, and read only once it has returned.
	var out *plugin.FragmentRender
	var renderErr error
	served, status, err := handler.ServeFragment(forFragment, func(w http.ResponseWriter, r *http.Request) {
		out, renderErr = a.renderFragmentFor(w, r)
	})
	if err != nil {
		return nil, err
	}
	// A middleware answered: the request never reached the page, or the
	// middleware answered over the render — a recovering one, a timeout — so
	// the reader is refused as a guard would refuse them.
	if !served {
		return nil, fmt.Errorf("%w: a middleware answered %d for %q", types.ErrGuardRefused, status, target.Path)
	}
	if renderErr != nil {
		return nil, renderErr
	}
	if out == nil {
		return nil, fmt.Errorf("collage: fragment %q rendered nothing", target.Path)
	}
	return out, nil
}

// maxFragmentDepth is how many RenderFragment calls may be under way within one
// another's middleware chain: a middleware rendering a fragment for the request
// it was handed is one level more.
const maxFragmentDepth = 4

// errFragmentDepth is RenderFragment's answer past maxFragmentDepth.
var errFragmentDepth = errors.New("collage: RenderFragment called from the middleware of a fragment it is rendering")

// fragmentDepthKey marks a fragment request's context with how many
// RenderFragment calls it is within.
type fragmentDepthKey struct{}

func fragmentDepth(ctx context.Context) int {
	depth, _ := ctx.Value(fragmentDepthKey{}).(int)
	return depth
}

// fragmentContext is the context of a fragment request made for a caller whose
// context is parent: it ends when parent does, with parent's deadline and cause,
// but it carries none of parent's values. parent is the stream's, usually, and
// what the stream's middleware put there — a signed-in user, a "this path is
// public" mark — was decided for the stream's path. A request to the fragment's
// URL carries what its own middleware puts there, and so does this one.
//
// What a request from the wire has before any middleware runs is kept: the
// server and the local address net/http puts there, and whether this is a
// static build's capture. The depth marks how many renders deep this is, and
// the render waits for fresh data: a plugin re-renders a fragment because an
// invalidation touched it, and pushes what it gets to readers who will not ask
// again, so a stale value served here would be the push, and the refresh that
// replaced it a moment later would reach nobody. A trace span parent carries is
// not kept: the tracer's key is its own, and a push is no request to trace.
//
// done releases it, and must be called when the render is over.
func fragmentContext(parent context.Context, depth int) (context.Context, context.CancelFunc) {
	base := context.Background()
	if server := parent.Value(http.ServerContextKey); server != nil {
		base = context.WithValue(base, http.ServerContextKey, server)
	}
	if local := parent.Value(http.LocalAddrContextKey); local != nil {
		base = context.WithValue(base, http.LocalAddrContextKey, local)
	}
	if types.IsCapture(parent) {
		base = types.WithCapture(base)
	}
	base = context.WithValue(base, fragmentDepthKey{}, depth)
	base = types.WithFreshData(base)

	ctx, cancel := context.WithCancelCause(base)
	stop := context.AfterFunc(parent, func() { cancel(context.Cause(parent)) })
	release := func() {}
	if deadline, ok := parent.Deadline(); ok {
		var cancelDeadline context.CancelFunc
		ctx, cancelDeadline = context.WithDeadline(ctx, deadline)
		release = cancelDeadline
	}
	return ctx, func() {
		stop()
		release()
		cancel(context.Canceled)
	}
}

// errStarting is RenderFragment's answer while the application is starting.
var errStarting = errors.New("collage: RenderFragment called while the application is starting; " +
	"the middleware it runs is not composed until the start has finished")

// servingHandler returns the handler a request is served by, starting the
// application first if nothing has. Not while a start is running: that start
// may be the caller's own — a plugin rendering from Init — and waiting for it
// would wait forever, so it is refused instead.
func (a *App) servingHandler() (*httpx.Handler, error) {
	if handler := a.warmer.Load(); handler != nil {
		return handler, nil
	}
	if !a.buildMu.TryLock() {
		return nil, errStarting
	}
	a.buildMu.Unlock()
	if err := a.Start(); err != nil {
		return nil, err
	}
	if handler := a.warmer.Load(); handler != nil {
		return handler, nil
	}
	return nil, errStarting
}

// renderFragmentFor renders the fragment r's URL reaches, for r: the request the
// middleware chain handed its innermost handler, so what a middleware put in its
// context is there. It writes the status a request would have been answered
// with to w, which goes nowhere but to the middleware around it — an access log,
// say — and nothing else.
func (a *App) renderFragmentFor(w http.ResponseWriter, r *http.Request) (*plugin.FragmentRender, error) {
	out, status, err := a.renderFragmentAt(w.Header(), r)
	w.WriteHeader(status)
	return out, err
}

// renderFragmentAt is renderFragmentFor's body, returning the status for it to
// write.
func (a *App) renderFragmentAt(header http.Header, r *http.Request) (*plugin.FragmentRender, int, error) {
	// Resolved again, from the request the chain passed on: a middleware may
	// have rewritten its URL, and a request to the URL is served at the one it
	// was rewritten to.
	at, err := a.fragmentAt(r)
	if err != nil {
		return nil, http.StatusNotFound, err
	}
	page, fragment := at.page, at.fragment
	// The route's parameters, on the request as serve puts them there, after the
	// middleware: a guard reads {id} with r.PathValue("id"). r was built for this
	// URL with no values of its own, so these are all it carries.
	for name, value := range at.params {
		r.SetPathValue(name, value)
	}
	// The fragment path's guards, as a request to its URL would meet them. A refusal
	// has no response to be written to here, so it comes back as an error the
	// plugin reads as "send this reader nothing".
	decision, err := fragmentGuard(r.Context(), r, page, fragment)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if decision != nil {
		return nil, decision.Status, fmt.Errorf("%w: fragment %q answered %d", types.ErrGuardRefused, fragment.Name, decision.Status)
	}

	renderer, ok := a.renderer.(render.FragmentResultRenderer)
	if !ok {
		return nil, http.StatusInternalServerError, fmt.Errorf("collage: this application's renderer cannot render a fragment in parts")
	}
	// A page served from one cached render to every reader has said its
	// handlers answer the same for everyone; a fragment of it can say no less.
	//
	// Said, not trusted: a render reported Shared is pushed to every reader of
	// the URL, so it is made the way the page cache makes its one render — from
	// a request with nothing of this reader's in it. Made from r itself, a
	// handler reading a cookie or a middleware's context value wrote this
	// reader's name into every other reader's page.
	shared := page.Strategy.Cacheable() || readerFree(fragment)
	renderFor := r
	if shared {
		var ok bool
		renderFor, ok = httpx.ShareableRequest(r, page.CacheParams, a.devMode, a.logger)
		if !ok {
			shared, renderFor = false, r
		}
	}
	renderNow := func(ctx context.Context) (*render.FragmentResult, error) {
		rc := types.NewRenderContext(ctx, renderFor, page, at.locale, at.params)
		return renderer.RenderFragmentResult(ctx, rc, fragment)
	}
	var result *render.FragmentResult
	if shared && a.memoizes() {
		// This reader passed the chain and the guards; the render every reader
		// so allowed gets is the one another's call may have made already.
		result, err = a.fragmentMemo.get(renderFor.Context(), a.memoKey(renderFor, at), a.memoAge(page), memoTags(renderFor), renderNow)
	} else {
		result, err = renderNow(renderFor.Context())
	}
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	out := &plugin.FragmentRender{
		HTML:           result.HTML,
		Head:           slices.Clone(result.Head),
		DependencyTags: slices.Clone(result.DependencyTags),
		Shared:         shared,
	}
	var token string
	if a.csrf != nil {
		if a.csrf.Carries(out.HTML) {
			var minted bool
			var err error
			token, minted, err = a.csrf.TokenFor(r)
			if err != nil {
				return nil, http.StatusInternalServerError, fmt.Errorf("collage: could not issue a forgery token: %w", err)
			}
			out.HTML = a.csrf.Personalise(out.HTML, token)
			out.Shared = false
			if minted {
				out.Cookie = a.csrf.Cookie(r, token)
			}
		}
	}
	// After the token, before the ETag, as a response to the path is made: a
	// hook's nonce or signed stamp is in the body the ETag names.
	personal, err := a.personaliseFragment(header, r, out)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if personal {
		out.Shared = false
	}
	// The ETag a request to the fragment's path would be answered with: the hash
	// of the same body, built the same way, after the same token went in.
	out.ETag = cache.ETag((&render.FragmentResult{HTML: out.HTML, Head: out.Head}).Body())
	// Masked after the ETag, as a response is: see csrf.Guard.Mask.
	if token != "" {
		out.HTML = a.csrf.Mask(out.HTML, token)
	}
	return out, http.StatusOK, nil
}

// memoizes reports whether RenderFragment keeps its Shared renders: not in
// development, where a template edit must show on the next push.
func (a *App) memoizes() bool {
	return a.fragmentMemo != nil && !a.devMode
}

// memoKey is what one Shared render of the fragment at r's URL is kept under:
// the URL as the router keys it — the path, its parameters and locale, the
// query the page caches on (all of r's there is, from ShareableRequest) — and
// the host, as the page cache keys a render. The build is not in it: the memo
// lives as long as the process.
func (a *App) memoKey(r *http.Request, at fragmentMatch) string {
	return cache.Key(cache.KeyInput{
		Path:   r.URL.Path,
		Locale: at.locale,
		Params: at.params,
		Vary:   []string{"fragment:" + at.page.Name + "/" + at.fragment.Name, "query:" + r.URL.RawQuery},
		Host:   ascii.LowerString(r.Host),
	})
}

// memoAge is how long a Shared render of a fragment of page is reused: the
// page's cache lifetime for a page cached for everyone — until invalidated for a
// Static one — and memoWindow for a page rendered per request, and for every
// page with the cache off, where every request renders. With a store shared
// between instances, memoWindow at most: an invalidation another instance made
// reaches its store, not this memo.
func (a *App) memoAge(page *types.Page) time.Duration {
	if !page.Strategy.Cacheable() || a.store == nil {
		return memoWindow
	}
	age := page.CacheTTL
	if _, shared := a.store.(cache.SharedCache); shared && (age == 0 || age > memoWindow) {
		age = memoWindow
	}
	return age
}

// memoTags are the tags beside its own a kept render is dropped by: those an
// invalidation by the URL's path or host names, as for a cached page.
func memoTags(r *http.Request) []string {
	return []string{types.PathTag(r.URL.Path), types.HostTag(ascii.LowerString(r.Host))}
}

// personaliseFragment runs every PersonaliseHook over out's markup and over each
// item it hoisted, and reports whether a hook made any of them this reader's.
//
// Over HTTP the hooks see one body, the hoisted items written into it; here the
// parts travel apart, so each is rewritten on its own. The marker a hook
// replaces — a CSP nonce's, a honeypot's stamp — is as likely in a hoisted
// script as in the markup, and is a key-derived secret wherever it is left.
func (a *App) personaliseFragment(header http.Header, r *http.Request, out *plugin.FragmentRender) (bool, error) {
	personalise := func(body []byte) ([]byte, bool, error) {
		ev := &plugin.PersonaliseEvent{Request: r, Header: header, Body: body}
		if err := a.plugins.Personalise(r.Context(), ev); err != nil {
			return nil, false, err
		}
		return ev.Body, ev.Personal, nil
	}
	body, personal, err := personalise(out.HTML)
	if err != nil {
		return false, err
	}
	out.HTML = body
	for i, item := range out.Head {
		body, itemPersonal, err := personalise([]byte(item.HTML))
		if err != nil {
			return false, err
		}
		// The hook's output, trusted as the body over HTTP is: a hook writes
		// markup, and the item was markup before it.
		out.Head[i].HTML = template.HTML(body)
		personal = personal || itemPersonal
	}
	return personal, nil
}

// fragmentTarget is the URL req names: its Path, or, for a fragment named by
// page and fragment, the URL the fragment's path in req's locale makes of
// req.Params — built as FragmentURL builds it, so parameters the router could
// never have captured (a "/" outside a catch-all, ".", "..", an empty value, a
// name the pattern has no placeholder for) reach no fragment, as no request
// could carry them.
func (a *App) fragmentTarget(req plugin.FragmentRequest) (*url.URL, error) {
	path := req.Path
	if path == "" {
		var err error
		if path, err = a.namedFragmentPath(req); err != nil {
			return nil, err
		}
	}
	target, err := url.Parse(path)
	if err != nil || target.IsAbs() || target.Host != "" || !strings.HasPrefix(target.Path, "/") {
		return nil, fmt.Errorf("%w: %q is not a path on this site", types.ErrUnknownFragmentPath, path)
	}
	// A path ServeHTTP would redirect to its clean spelling is not served at
	// all; a middleware deciding on "/admin" must not meet "/x/../admin".
	if !httpx.IsCleanPath(target.Path) {
		return nil, fmt.Errorf("%w: %q is not a clean path", types.ErrUnknownFragmentPath, path)
	}
	return target, nil
}

// namedFragmentPath is the URL of req's fragment in req's locale. A fragment
// opened at several paths in the locale is given the first, in pattern order,
// that req.Params fill.
func (a *App) namedFragmentPath(req plugin.FragmentRequest) (string, error) {
	locale := req.Locale
	if locale == "" {
		locale = a.cfg.Locale.Default
	}
	if !a.localeReachable(locale) {
		return "", fmt.Errorf("%w: %q", ErrLocaleUnreachable, locale)
	}
	a.mu.RLock()
	page := a.pages[req.Page]
	a.mu.RUnlock()
	if page == nil {
		return "", fmt.Errorf("%w: %q", types.ErrUnknownRoute, req.Page)
	}
	var patterns []string
	for pattern, f := range page.FragmentPaths[locale] {
		if f != nil && f.Name == req.Fragment {
			patterns = append(patterns, pattern)
		}
	}
	if len(patterns) == 0 {
		return "", fmt.Errorf("%w: %q on page %q in %q", types.ErrUnknownFragmentPath, req.Fragment, req.Page, locale)
	}
	slices.Sort(patterns)
	var errs []error
	for _, pattern := range patterns {
		path, err := router.BuildPath(pattern, req.Params)
		if err == nil {
			return a.prefixLocale(path, locale), nil
		}
		errs = append(errs, err)
	}
	return "", fmt.Errorf("%w: %q on page %q: %w", types.ErrUnknownFragmentPath, req.Fragment, req.Page, errors.Join(errs...))
}

// fragmentRequest is the request a reader would send for target: a GET carrying
// r's host, client address, TLS state, headers and cookies — what decides who
// the reader is. Not r's context: see fragmentContext.
//
// Built fresh rather than cloned: a clone keeps r's path values, and a guard
// deciding on r.PathValue would judge the stream's values, not the fragment's.
// The headers that describe r's own body, its upgrade to a WebSocket or a
// condition on r's own response are not the fragment's, and are left out.
func fragmentRequest(r *http.Request, target *url.URL) *http.Request {
	header := r.Header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	for name := range header {
		switch canonical := http.CanonicalHeaderKey(name); {
		case strings.HasPrefix(canonical, "Sec-Websocket-"), strings.HasPrefix(canonical, "If-"),
			strings.HasPrefix(canonical, "Content-"):
			delete(header, name)
		}
	}
	for _, name := range []string{"Upgrade", "Connection", "Transfer-Encoding", "Expect", "Range", "Trailer", "Te"} {
		header.Del(name)
	}
	u := &url.URL{Path: target.Path, RawPath: target.RawPath, RawQuery: target.RawQuery}
	out := &http.Request{
		Method:     http.MethodGet,
		URL:        u,
		Proto:      r.Proto,
		ProtoMajor: r.ProtoMajor,
		ProtoMinor: r.ProtoMinor,
		Header:     header,
		Body:       http.NoBody,
		Host:       r.Host,
		RemoteAddr: r.RemoteAddr,
		RequestURI: u.RequestURI(),
		TLS:        r.TLS,
	}
	// Its context is the caller's to give it: see fragmentContext.
	return out
}

// fragmentMatch is what a fragment path's URL resolved to.
type fragmentMatch struct {
	page     *types.Page
	fragment *types.Fragment
	locale   string
	params   map[string]string
}

// fragmentAt resolves r's URL as the router would, to the fragment a page opened
// at it, or ErrUnknownFragmentPath.
func (a *App) fragmentAt(r *http.Request) (fragmentMatch, error) {
	match, err := a.routes.Match(r)
	if err != nil || match == nil || match.Action == nil {
		return fragmentMatch{}, fmt.Errorf("%w: %q", types.ErrUnknownFragmentPath, r.URL.RequestURI())
	}
	a.mu.RLock()
	found, ok := a.fragmentPaths[match.Action]
	a.mu.RUnlock()
	if !ok {
		return fragmentMatch{}, fmt.Errorf("%w: %q", types.ErrUnknownFragmentPath, r.URL.RequestURI())
	}
	return fragmentMatch{page: found.page, fragment: found.fragment, locale: match.Locale, params: match.PathParams}, nil
}

// fetchFree reports whether nothing in f's subtree renders differently per
// request: no data handler that is not declared Static, no slot resolver. The same
// test resolveStrategy makes of a whole page, made of one fragment.
func fetchFree(f *types.Fragment) bool {
	return subtreeFree(f, func(f *types.Fragment) bool { return f.Static })
}

// readerFree reports whether nothing in f's subtree renders differently per
// reader: every data handler is declared Static or Shared, and there is no slot
// resolver. What it renders may still change over time.
func readerFree(f *types.Fragment) bool {
	return subtreeFree(f, func(f *types.Fragment) bool { return f.Static || f.Shared })
}

// subtreeFree reports whether every data handler in f's subtree is one trusted
// says may be discounted, and no fragment in it has a slot resolver.
func subtreeFree(f *types.Fragment, trusted func(*types.Fragment) bool) bool {
	fetches := errors.New("fetches per render")
	err := walkFragments(f, make(map[*types.Fragment]bool), func(f *types.Fragment) error {
		if types.FragmentDataSource(f).Handler != nil && !trusted(f) {
			return fetches
		}
		for _, slot := range f.Slots {
			if slot != nil && slot.Resolve != nil {
				return fetches
			}
		}
		return nil
	})
	return err == nil
}

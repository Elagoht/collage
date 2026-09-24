package core

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// ErrDuplicateDocument is returned when a document is registered under a name
// another document already holds. It is deliberately its own sentinel rather than
// ErrDuplicatePage: pages and documents are named in separate registries, reached
// through Page/Pages and Documents respectively, so a page and a document may share
// a name without conflict — only two documents colliding on Name is an error.
var ErrDuplicateDocument = errors.New("collage: duplicate document name")

// ErrDocumentNotFound is returned by RenderDocumentPath when path resolves to no
// document — either nothing matched, what matched is a page rather than a document,
// or what matched is a redirect. All three mean the same thing to the caller: there
// is no document to render at that path. It mirrors ErrPageNotFound for the same
// reason RenderDocumentPath mirrors RenderPath.
var ErrDocumentNotFound = errors.New("collage: no document at path")

// documentRenderer is the capability RenderDocumentPath needs beyond render.Engine's
// page-only Render method. It is declared locally, and reached from a.renderer
// through a type assertion, rather than added to render.Engine itself — the same
// reasoning internal/httpx's own documentRenderer documents: widening render.Engine
// would force every implementation, including a page-only test double, to grow a
// method it does not need. *render.SlotEngine, the only renderer New ever
// constructs, already satisfies it via ExecuteDocument, so the assertion below
// cannot fail for an App built through New — it exists so a future renderer
// injection point fails loudly rather than panicking on a missing method.
type documentRenderer interface {
	// ExecuteDocument runs doc's handler and returns its body, content type and
	// dependency tags. See render.SlotEngine.ExecuteDocument.
	ExecuteDocument(ctx context.Context, doc *types.Document, rc *types.RenderContext) (*render.DocumentResult, error)
}

// RegisterDocument validates doc and adds it to the router. It returns
// ErrAppStarted after the server has started, ErrNilDocument for a nil document,
// ErrDuplicateDocument for a repeated name, ErrDuplicateRoute when a path collides
// with a page or another document, and the document's own validation error
// otherwise — always naming the document.
//
// It mirrors RegisterPage's prepare exactly, minus the layout binding and the
// template-existence check: a document has no template and no layout, so
// types.Document.Validate alone is enough to catch every registration-time mistake.
func (a *App) RegisterDocument(doc *types.Document) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.prepareDocument(doc); err != nil {
		return err
	}
	if err := a.routes.RegisterDocument(doc); err != nil {
		return err
	}
	a.rememberDocument(doc)
	return nil
}

// prepareDocument runs every check RegisterDocument needs before handing doc to the
// router. It must be called with a.mu held.
//
// A document that is already registered under its own name — the same pointer, not
// merely the same name — passes without error, mirroring prepare's own treatment of
// a page registered twice under the same name.
func (a *App) prepareDocument(doc *types.Document) error {
	if doc == nil {
		return types.ErrNilDocument
	}
	if a.started {
		return fmt.Errorf("%w: cannot register document %q", ErrAppStarted, doc.Name)
	}
	// Checked before anything else uses the name in a message or as a map key: an
	// unnamed document cannot be reported on, looked up, or told apart from
	// another — the same reasoning prepare gives for pages.
	if doc.Name == "" {
		return types.ErrEmptyName
	}
	if existing, ok := a.documents[doc.Name]; ok && existing != doc {
		return fmt.Errorf("%w: %q", ErrDuplicateDocument, doc.Name)
	}
	if err := types.DocumentBuildErr(doc); err != nil {
		return fmt.Errorf("collage: document %q was built with errors: %w", doc.Name, err)
	}
	if err := doc.Validate(); err != nil {
		return fmt.Errorf("collage: document %q: %w", doc.Name, err)
	}
	return nil
}

// rememberDocument records doc in the document registry under its name, preserving
// registration order for Documents. It must be called with a.mu held, and is a
// no-op for a document already registered under that name.
func (a *App) rememberDocument(doc *types.Document) {
	if _, ok := a.documents[doc.Name]; ok {
		return
	}
	a.documents[doc.Name] = doc
	a.docOrder = append(a.docOrder, doc.Name)
}

// Documents returns every registered document, in registration order, as a copy of
// the slice: appending to or reordering the returned slice does not affect the
// application.
//
// Unlike Pages, the documents themselves are handed back by the same pointer they
// were registered with rather than defensively copied. Pages defends against a
// plugin writing through Page's mutable maps and slices because Pages is part of
// plugin.Host; Documents is not — nothing outside this package reaches it except
// the static builder, which only reads it — and types.Document carries no
// framework-owned state analogous to a page's bound layout for a copy to protect.
func (a *App) Documents() []*types.Document {
	a.mu.RLock()
	defer a.mu.RUnlock()

	docs := make([]*types.Document, 0, len(a.docOrder))
	for _, name := range a.docOrder {
		docs = append(docs, a.documents[name])
	}
	return docs
}

// RenderDocumentPath renders the document registered at path, outside the HTTP
// request path and bypassing the cache entirely, and returns the raw execution
// result. It is RenderPath's sibling for documents, and follows the same rules:
//
// The document is resolved through the router, from a synthetic GET request for
// path, so a path reaches exactly the document it would reach over HTTP. A
// non-empty locale is a *request* for that locale, offered to the router the same
// way RenderPath offers one — see syntheticRequest.
//
// The locale the document actually executes for is whatever the router resolved,
// not the argument: match.Locale, never locale. RenderPath was corrected on exactly
// this point — a caller-supplied locale that disagrees with the matched route must
// not execute one document's handler while telling it a different locale — and
// RenderDocumentPath follows the same rule for the same reason.
//
// params overlay the path parameters the router captured, exactly as in RenderPath.
//
// It returns ErrDocumentNotFound when path resolves to no document, including when
// it resolves to a page or a redirect, and the handler's own error when execution
// fails.
func (a *App) RenderDocumentPath(ctx context.Context, path, locale string, params map[string]string) (*render.DocumentResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Startup first, memoised — see RenderPath for why.
	if _, err := a.buildHandler(); err != nil {
		return nil, err
	}

	req := a.syntheticRequest(ctx, path, locale)

	match, err := a.routes.Match(req)
	if err != nil {
		return nil, fmt.Errorf("collage: match %q: %w", path, err)
	}
	if match == nil || match.Document == nil {
		if match != nil && match.RedirectTo != "" {
			return nil, fmt.Errorf("%w: %q redirects to %q", ErrDocumentNotFound, path, match.RedirectTo)
		}
		return nil, fmt.Errorf("%w: %q", ErrDocumentNotFound, path)
	}

	docRenderer, ok := a.renderer.(documentRenderer)
	if !ok {
		return nil, fmt.Errorf("collage: renderer does not support document execution: %T", a.renderer)
	}

	merged := make(map[string]string, len(match.PathParams)+len(params))
	maps.Copy(merged, match.PathParams)
	maps.Copy(merged, params)

	// match.Locale, not the locale argument — see the doc comment above.
	rc := types.NewRenderContext(ctx, req, nil, match.Locale, merged)

	result, err := docRenderer.ExecuteDocument(ctx, match.Document, rc)
	if err != nil {
		return result, err
	}

	// Dispatched here as well as on the serving path, so a static build writes what
	// the server would have sent. See RenderPath for the reasoning.
	event := &plugin.DocumentRenderedEvent{
		Document:    match.Document,
		ContentType: match.Document.ContentType,
		Locale:      match.Locale,
		Path:        path,
		Body:        result.Body,
	}
	if err := a.plugins.DocumentRendered(ctx, event); err != nil {
		return nil, err
	}
	result.Body = event.Body
	return result, nil
}

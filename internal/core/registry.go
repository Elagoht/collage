package core

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/types"
)

// RegisterPage validates p, binds its content fragment into its layout, checks that
// every template it names exists, and registers its paths and redirects with the
// router.
//
// This is where the framework's "no silent failures" invariant is enforced. A page
// that would fail at render time fails here instead, at startup, with an error
// naming the page and the specific problem:
//
//   - a page that does not pass types.Page.Validate — an empty name, no content
//     fragment, a path that does not start with "/", an incoherent strategy and TTL
//     combination, a required slot with nothing bound to it, a fragment cycle;
//   - a fragment naming a template the engine has not loaded (ErrTemplateNotFound),
//     which turns a typo in a template path from a first-request 500 into a startup
//     error;
//   - a name another page already holds (ErrDuplicatePage);
//   - registration after the application has started (ErrAppStarted).
//
// The templates of the page's own NotFoundPage and ErrorPage are checked here too.
// Those pages are reached straight off the Page's fields, whether or not they were
// ever registered in their own right, and a typo in a 500 page's template would
// otherwise surface only once the site is already failing — the worst possible
// moment to discover it. Only their templates are checked: they get no routes of
// their own here, and their own error pages are not recursed into.
//
// The order of those checks is deliberate. Binding happens before Validate because a
// layout that declares its content slot Required — which is the idiomatic way to
// write one — does not validate until the content fragment is actually in it.
//
// A rejected registration is not rolled back: a page whose paths were accepted for
// one locale and refused for another leaves the accepted routes in the router. A
// failed RegisterPage is a startup failure the embedding program is expected to
// abort on, not something to recover from and carry on serving with.
func (a *App) RegisterPage(p *types.Page) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.prepare(p); err != nil {
		return err
	}
	if err := a.routes.Register(p); err != nil {
		return err
	}
	// After the page's own paths are in the tree: an attached action inherits
	// them, so there is nothing to inherit until they exist.
	if err := a.registerPageActions(p); err != nil {
		return err
	}
	if err := a.registerFragmentPaths(p); err != nil {
		return err
	}
	a.remember(p)
	return nil
}

// RegisterNotFoundPage registers p as the page served when a request resolves to no
// content. p goes through exactly the same validation, binding, and template checks
// as RegisterPage; it is not given routes of its own, since it is reached by failing
// to match rather than by matching.
func (a *App) RegisterNotFoundPage(p *types.Page) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.prepare(p); err != nil {
		return err
	}
	if err := a.routes.RegisterNotFound(p); err != nil {
		return err
	}
	a.remember(p)
	return nil
}

// RegisterErrorPage registers p as the page served when rendering fails. p goes
// through exactly the same validation, binding, and template checks as
// RegisterPage; like the not-found page it is not given routes of its own.
func (a *App) RegisterErrorPage(p *types.Page) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.prepare(p); err != nil {
		return err
	}
	if err := a.routes.RegisterError(p); err != nil {
		return err
	}
	a.remember(p)
	return nil
}

// prepare runs every check the three page registration methods share and performs
// the layout/content binding. It must be called with a.mu held.
//
// A page that is already registered under its own name — the same pointer, not
// merely the same name — passes without being bound a second time, so a page
// registered with RegisterPage and then handed to RegisterNotFoundPage is accepted
// rather than reported as a duplicate of itself.
func (a *App) prepare(p *types.Page) error {
	if p == nil {
		return ErrNilPage
	}
	if a.started {
		return fmt.Errorf("%w: cannot register page %q", ErrAppStarted, p.Name)
	}
	// Checked before anything else uses the name in a message or as a map key: an
	// unnamed page cannot be reported on, looked up, or told apart from another.
	if p.Name == "" {
		return types.ErrEmptyName
	}
	if existing, ok := a.pages[p.Name]; ok && existing != p {
		return fmt.Errorf("%w: %q", ErrDuplicatePage, p.Name)
	}
	// What a builder recorded is refused here whether or not anyone asked the
	// builder: a slot declared twice or a fragment bound to a slot that does not
	// exist is a page that does not say what its author wrote.
	if err := types.PageBuildErr(p); err != nil {
		return fmt.Errorf("collage: page %q was built with errors: %w", p.Name, err)
	}

	if err := a.bindContent(p); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("collage: page %q: %w", p.Name, err)
	}
	if err := a.checkTemplates(p); err != nil {
		return err
	}
	if p.NotFoundPage != nil {
		if err := a.checkTemplates(p.NotFoundPage); err != nil {
			return fmt.Errorf("collage: page %q not-found page: %w", p.Name, err)
		}
	}
	if p.ErrorPage != nil {
		if err := a.checkTemplates(p.ErrorPage); err != nil {
			return fmt.Errorf("collage: page %q error page: %w", p.Name, err)
		}
	}
	return nil
}

// remember records p in the page registry under its name, preserving registration
// order for Pages. It must be called with a.mu held, and is a no-op for a page
// already registered under that name.
func (a *App) remember(p *types.Page) {
	if _, ok := a.pages[p.Name]; ok {
		return
	}
	a.pages[p.Name] = p
	a.order = append(a.order, p.Name)
}

// bindContent gives p its own private copy of its layout fragment and binds p's
// content fragment into that copy's content slot, replacing p.LayoutFragment with
// it so Page.Root returns the bound copy. A page with no layout fragment needs no
// binding: its content fragment is already the root.
//
// The copy is what makes a layout shareable, and a layout is the most reusable
// object a component framework has — a typical blog uses one layout for a post page
// and its 404 and 500 pages. types.Fragment.Bind
// appends to the SlotDefinition's Fill, and a SlotDefinition reached through a
// shared layout is one object: binding three pages' content into it would leave
// three fills in one slot and render all three pages' content on every one of them.
// Copying the slot table per page makes each page's bindings private.
//
// Only the layout's own slot table is copied — a fresh Slots map holding fresh
// SlotDefinition values with copied Fill slices. Everything else stays shared by
// pointer: the template path, the data handler, the fallback, and every child
// fragment already bound into a slot. Registration therefore snapshots the layout's
// bindings: a fragment bound into the shared layout after a page was registered
// does not appear on that page.
//
// Binding also happens exactly once per page. The slot's fills render in binding
// order, so binding the same content fragment twice would render the page's content
// twice. The "already done" test is a.bound — the set of pages whose layout this App
// has already replaced with their own copy — and deliberately not "the slot already
// holds this content fragment": a caller who hand-bound the content into a shared
// layout satisfies the latter while still pointing at the shared layout, so taking
// that as done would leave the page sharing its slot table and make the next page on
// that layout fail. What a hand-bound layout does skip is the Bind itself, since the
// copy already carries the fill.
//
// It must be called with a.mu held.
func (a *App) bindContent(p *types.Page) error {
	if p.LayoutFragment == nil {
		return nil
	}
	if p.ContentFragment == nil {
		return fmt.Errorf("collage: page %q: %w", p.Name, types.ErrMissingContent)
	}
	if a.bound[p] {
		return nil
	}

	layout := copyLayout(p.LayoutFragment)
	if !contentBound(layout, p.ContentFragment) {
		if err := layout.Bind(types.DefaultContentSlot, p.ContentFragment); err != nil {
			return fmt.Errorf(
				"collage: page %q: binding content fragment %q into layout fragment %q slot %q (registration fills the content slot itself, so a layout must not have it filled already): %w",
				p.Name, p.ContentFragment.Name, p.LayoutFragment.Name, types.DefaultContentSlot, err,
			)
		}
	}
	p.LayoutFragment = layout
	a.bound[p] = true
	return nil
}

// contentBound reports whether layout's content slot already holds content.
func contentBound(layout, content *types.Fragment) bool {
	slot, ok := layout.Slot(types.DefaultContentSlot)
	return ok && slices.Contains(slot.Fill, content)
}

// copyLayout returns a shallow copy of f carrying a slot table of its own: a fresh
// Slots map, a fresh SlotDefinition value per entry, and a copied Fill slice per
// slot. Nothing else is duplicated — the copy renders the same template, runs the
// same data handler, and holds the same child fragments — so the only state that
// becomes private is which fragments this page binds into which slot. See
// bindContent for why that is the boundary.
func copyLayout(f *types.Fragment) *types.Fragment {
	copied := *f
	if f.Slots == nil {
		return &copied
	}

	copied.Slots = make(map[string]*types.SlotDefinition, len(f.Slots))
	for name, slot := range f.Slots {
		if slot == nil {
			copied.Slots[name] = nil
			continue
		}
		value := *slot
		value.Fill = slices.Clone(slot.Fill)
		copied.Slots[name] = &value
	}
	return &copied
}

// checkTemplates reports ErrTemplateNotFound for the first fragment of p whose
// TemplatePath the engine has not loaded, naming the page, the fragment, and the
// path.
//
// It walks the layout tree and the content tree separately rather than just
// p.Root(): p is not always a page being registered in its own right — it may be
// another page's NotFoundPage or ErrorPage, which has not been through binding yet,
// so its content fragment is not in its layout's slot and Root alone would miss it.
// The shared visited set means a fragment reachable both ways is still checked once.
func (a *App) checkTemplates(p *types.Page) error {
	visited := make(map[*types.Fragment]bool)
	visit := func(f *types.Fragment) error {
		if a.tmpl.Lookup(f.TemplatePath) {
			return nil
		}
		return fmt.Errorf("%w: page %q fragment %q references %q",
			ErrTemplateNotFound, p.Name, f.Name, f.TemplatePath)
	}

	if err := walkFragments(p.LayoutFragment, visited, visit); err != nil {
		return err
	}
	if err := walkFragments(p.ContentFragment, visited, visit); err != nil {
		return err
	}
	// A fragment opened at its own URL may be reachable from nowhere else, and
	// unchecked it answered with an empty 200 for a template that did not exist.
	for _, fragment := range p.PathFragments() {
		if err := walkFragments(fragment, visited, visit); err != nil {
			return err
		}
	}
	return nil
}

// walkFragments calls visit on f and on every fragment reachable from it, through
// slot fills and fallbacks, stopping at the first error. visited makes the walk
// terminate on any graph, not only the acyclic ones Fragment.Validate accepts:
// checkTemplates runs before that guarantee is worth relying on, and a fragment
// reached twice has nothing new to check either way.
func walkFragments(f *types.Fragment, visited map[*types.Fragment]bool, visit func(*types.Fragment) error) error {
	if f == nil || visited[f] {
		return nil
	}
	visited[f] = true

	if err := visit(f); err != nil {
		return err
	}
	for _, name := range f.SlotNames() {
		slot := f.Slots[name]
		if slot == nil {
			continue
		}
		for _, child := range slot.Fill {
			if err := walkFragments(child, visited, visit); err != nil {
				return err
			}
		}
	}
	return walkFragments(f.Fallback, visited, visit)
}

// checkErrorPagesRegistered reports ErrUnregisteredErrorPage for the first
// registered page — including the global not-found and error pages, which are
// registered like any other — that references a NotFoundPage or ErrorPage never
// registered in its own right.
//
// A referenced page is served straight off the Page's field, so an unregistered one
// never goes through registration's binding step: its content fragment is never put
// into its layout's content slot. The layout then renders with an empty slot, the
// handler logs "error page rendered empty", and the visitor gets the built-in page
// instead of the author's — a custom error page that silently never appears, with
// the only signal a log line on a request that was already failing. Requiring
// registration is what makes that a startup error instead.
//
// Identity, not just the name, is what counts: the registered page must be the same
// object the field points at, since registration binds the layout copy into that
// object and a same-named impostor would not have been bound.
func (a *App) checkErrorPagesRegistered() error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	for _, name := range a.order {
		page := a.pages[name]
		if err := a.checkReferencedPage(page, page.NotFoundPage, "not-found page"); err != nil {
			return err
		}
		if err := a.checkReferencedPage(page, page.ErrorPage, "error page"); err != nil {
			return err
		}
	}
	return nil
}

// checkReferencedPage reports ErrUnregisteredErrorPage, naming both the referring
// page and the referenced one, unless referenced is nil or is itself registered. It
// must be called with a.mu held.
func (a *App) checkReferencedPage(from, referenced *types.Page, role string) error {
	if referenced == nil || a.pages[referenced.Name] == referenced {
		return nil
	}
	return fmt.Errorf("%w: page %q references the %s %q, which was never registered",
		ErrUnregisteredErrorPage, from.Name, role, referenced.Name)
}

// RegisterPlugin registers p with the application's plugin registry. Plugins must be
// registered before the application starts, since Init runs once, at startup, in
// registration order; afterwards this returns ErrAppStarted.
func (a *App) RegisterPlugin(p plugin.Plugin) error {
	a.mu.Lock()
	started := a.started
	a.mu.Unlock()

	if started {
		return ErrAppStarted
	}
	// A Configurer registered here has already missed its phase: Configure runs
	// inside New, before templates are parsed, and this is called afterwards.
	// Silently skipping it would leave a plugin that registered a template
	// function wondering why no template can call it.
	if _, ok := p.(plugin.Configurer); ok {
		return fmt.Errorf("%w: plugin %q", ErrConfigurerRegisteredLate, p.Name())
	}
	// A start that failed in a plugin's Init leaves the application unstarted
	// but its plugin registry closed. It is the same refusal, so it is the same
	// sentinel the caller already matches.
	if err := a.plugins.Register(p); err != nil {
		if errors.Is(err, plugin.ErrRegistryStarted) {
			return fmt.Errorf("%w: cannot register plugin %q", ErrAppStarted, p.Name())
		}
		return err
	}
	return nil
}

// RegisterCommand registers cmd as a CLI subcommand, rejecting an empty name
// (ErrEmptyCommandName) and a name another command already holds
// (ErrDuplicateCommand).
//
// Unlike the other registration methods it is not closed by ErrAppStarted: plugins
// contribute their commands from inside Init, which runs as part of starting the
// application.
func (a *App) RegisterCommand(cmd plugin.Command) error {
	if cmd.Name == "" {
		return ErrEmptyCommandName
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for i := range a.commands {
		if a.commands[i].Name == cmd.Name {
			return fmt.Errorf("%w: %q", ErrDuplicateCommand, cmd.Name)
		}
	}
	a.commands = append(a.commands, cmd)
	return nil
}

// Commands returns the CLI subcommands plugins contributed, in registration order,
// as a copy: appending to or reordering the returned slice does not affect the
// application.
func (a *App) Commands() []plugin.Command {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return slices.Clone(a.commands)
}

// Pages returns every registered page, in registration order, each a defensive copy
// as plugin.Host requires. See copyPage for exactly what is copied and what stays
// shared.
func (a *App) Pages() []*types.Page {
	a.mu.RLock()
	defer a.mu.RUnlock()

	pages := make([]*types.Page, 0, len(a.order))
	for _, name := range a.order {
		pages = append(pages, copyPage(a.pages[name]))
	}
	return pages
}

// Page returns the page registered under name, as a defensive copy on the same
// terms as Pages, and whether one was found.
func (a *App) Page(name string) (*types.Page, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	page, ok := a.pages[name]
	if !ok {
		return nil, false
	}
	return copyPage(page), true
}

// copyPage returns a defensive copy of p, as plugin.Host requires of Pages and Page.
// Without it a plugin holding a returned *types.Page could write straight through it
// into the framework's live state: types.Page is a plain struct of exported fields.
//
// What is copied: the Page struct itself, its Paths and SEO maps, its DependencyTags
// slice, and its Redirects — both the slice and the *types.Redirect values it points
// at. Copying only the slice would stop a plugin adding, removing, or reordering
// redirects but not mutating one through a shared element pointer, so the values are
// copied too.
//
// The SEO copy is one level deep, and deliberately so. Page.SEO is a
// map[string]any whose values are opaque to the framework: adding, replacing, or
// deleting a top-level key on the returned copy cannot reach the original, but a
// value that is itself a map, a slice, or a pointer is the same object the original
// holds, and writing *through* one of those reaches live framework state. Going
// deeper would need reflection, which this project does not use, and there is no
// type information to recurse on without it. A plugin that has to modify nested SEO
// metadata should replace the whole top-level value rather than mutate it in place.
//
// What stays shared, deliberately: LayoutFragment, ContentFragment, NotFoundPage,
// and ErrorPage. Fragment trees are not deep-copied — a plugin that mutates a
// fragment reached through one of these pointers is mutating live framework state,
// which plugin.Host documents as out of scope for this copy and which the package
// doc there calls undefined behaviour. NotFoundPage and ErrorPage are pages in their
// own right and are returned copied when they are asked for by name.
func copyPage(p *types.Page) *types.Page {
	if p == nil {
		return nil
	}

	copied := *p
	copied.Paths = maps.Clone(p.Paths)
	copied.SEO = maps.Clone(p.SEO)
	copied.DependencyTags = slices.Clone(p.DependencyTags)

	if p.Redirects != nil {
		copied.Redirects = make([]*types.Redirect, len(p.Redirects))
		for i, redirect := range p.Redirects {
			if redirect == nil {
				continue
			}
			value := *redirect
			copied.Redirects[i] = &value
		}
	}

	return &copied
}

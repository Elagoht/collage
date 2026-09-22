package core

import (
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

	if err := bindContent(p); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("collage: page %q: %w", p.Name, err)
	}
	return a.checkTemplates(p)
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

// bindContent binds p's content fragment into its layout fragment's content slot,
// exactly once. A page with no layout fragment needs no binding: its content
// fragment is already the root.
//
// "Exactly once" is load-bearing. The slot's fills render in binding order, so
// binding the same content fragment twice would render the page's content twice.
// bindContent therefore returns without doing anything when the slot already holds
// this page's content fragment, which makes a retried registration safe.
//
// Binding a second, different content fragment into the slot is a genuine error and
// is reported as one. In practice it means a layout fragment is being shared between
// pages: a Fragment is a value in a tree, not a template handle, so each page needs
// its own layout fragment even when both are built from the same template file.
func bindContent(p *types.Page) error {
	if p.LayoutFragment == nil {
		return nil
	}
	if p.ContentFragment == nil {
		return fmt.Errorf("collage: page %q: %w", p.Name, types.ErrMissingContent)
	}

	if slot, ok := p.LayoutFragment.Slot(types.DefaultContentSlot); ok {
		if slices.Contains(slot.Fill, p.ContentFragment) {
			return nil
		}
	}

	if err := p.LayoutFragment.Bind(types.DefaultContentSlot, p.ContentFragment); err != nil {
		return fmt.Errorf(
			"collage: page %q: binding content fragment %q into layout fragment %q slot %q (a layout fragment cannot be shared between pages; build one per page): %w",
			p.Name, p.ContentFragment.Name, p.LayoutFragment.Name, types.DefaultContentSlot, err,
		)
	}
	return nil
}

// checkTemplates reports ErrTemplateNotFound for the first fragment reachable from
// p's root whose TemplatePath the engine has not loaded, naming the page, the
// fragment, and the path. It runs after binding, so the content fragment and
// everything below it is reachable from the root and checked too.
func (a *App) checkTemplates(p *types.Page) error {
	visited := make(map[*types.Fragment]bool)
	return walkFragments(p.Root(), visited, func(f *types.Fragment) error {
		if a.tmpl.Lookup(f.TemplatePath) {
			return nil
		}
		return fmt.Errorf("%w: page %q fragment %q references %q",
			ErrTemplateNotFound, p.Name, f.Name, f.TemplatePath)
	})
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
	return a.plugins.Register(p)
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

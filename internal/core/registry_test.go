package core

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/types"
)

// TestApp_SatisfiesPluginHost is the compile-time assertion, restated as a test
// so the requirement is visible in the suite and not only in a var declaration:
// *App must be usable wherever a plugin expects its Host.
func TestApp_SatisfiesPluginHost(t *testing.T) {
	app := newTestApp(t, nil)

	var host plugin.Host = app
	if host.DevMode() {
		t.Fatal("DevMode through Host = true, want false for the default fixture")
	}
	if host.Logger() == nil {
		t.Fatal("Logger through Host = nil")
	}
	if pages := host.Pages(); len(pages) != 0 {
		t.Fatalf("Pages through Host = %v, want none before registration", pages)
	}
	if _, ok := host.Page("home"); ok {
		t.Fatal("Page through Host found a page before registration")
	}
	if err := host.InvalidateTags(context.Background(), "nothing"); err != nil {
		t.Fatalf("InvalidateTags through Host: %v", err)
	}
	if err := host.RegisterCommand(plugin.Command{Name: "demo"}); err != nil {
		t.Fatalf("RegisterCommand through Host: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RegisterPage: the no-silent-failures gate
// ---------------------------------------------------------------------------

// TestRegisterPage_BindsContentExactlyOnce is the binding invariant. The layout's
// content slot renders its fills in order, so a second binding would render the
// page's content twice. A repeated registration must therefore leave exactly one
// fill behind, whatever else it reports.
func TestRegisterPage_BindsContentExactlyOnce(t *testing.T) {
	app := newTestApp(t, nil)
	page := newHomePage()

	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	slot, ok := page.LayoutFragment.Slot(types.DefaultContentSlot)
	if !ok {
		t.Fatal("layout has no content slot after registration")
	}
	if len(slot.Fill) != 1 {
		t.Fatalf("content slot has %d fills, want exactly 1", len(slot.Fill))
	}
	if slot.Fill[0] != page.ContentFragment {
		t.Fatal("content slot holds a fragment other than the page's content fragment")
	}

	if err := app.RegisterPage(page); err == nil {
		t.Fatal("registering the same page twice = nil, want an error")
	}
	if len(slot.Fill) != 1 {
		t.Fatalf("content slot has %d fills after a rejected re-registration, want 1", len(slot.Fill))
	}
}

// TestRegisterPage_BindsOnceIntoAMultiFillSlot is the same invariant where the
// slot itself would not stop a second binding. A layout declaring its content slot
// AllowMultiple accepts every Bind it is handed, so nothing but the registration
// path's own "already bound" check keeps the page's content out of the slot twice —
// and a slot with two fills renders its content twice.
func TestRegisterPage_BindsOnceIntoAMultiFillSlot(t *testing.T) {
	app := newTestApp(t, nil)
	page := newHomePage()
	page.LayoutFragment.Slots[types.DefaultContentSlot].AllowMultiple = true

	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	// Designating the same page again re-runs the binding step, which is exactly
	// what the specification's own example does with a page it already registered.
	if err := app.RegisterErrorPage(page); err != nil {
		t.Fatalf("RegisterErrorPage: %v", err)
	}

	if got := len(page.LayoutFragment.Slots[types.DefaultContentSlot].Fill); got != 1 {
		t.Fatalf("content slot has %d fills, want exactly 1", got)
	}
	if got := strings.Count(get(app.Handler(), "/").Body.String(), "Welcome Home"); got != 1 {
		t.Fatalf("rendered content appears %d times, want once", got)
	}
}

// TestRegisterPage_RendersContentOnce proves the invariant above through the
// rendered output rather than through the data structure: the content appears in
// the served page exactly once.
func TestRegisterPage_RendersContentOnce(t *testing.T) {
	app := newTestApp(t, nil)
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	body := get(app.Handler(), "/").Body.String()
	if got := strings.Count(body, "Welcome Home"); got != 1 {
		t.Fatalf("rendered content appears %d times in %q, want once", got, body)
	}
}

// TestRegisterPage_WithoutLayout: a page with no layout needs no binding, and its
// content fragment is the root.
func TestRegisterPage_WithoutLayout(t *testing.T) {
	app := newTestApp(t, nil)
	page := newHomePage()
	page.LayoutFragment = nil

	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	body := get(app.Handler(), "/").Body.String()
	if !strings.Contains(body, "Welcome Home") {
		t.Fatalf("body = %q, want the content", body)
	}
	if strings.Contains(body, "<main>") {
		t.Fatalf("body = %q, want no layout markup", body)
	}
}

// TestRegisterPage_RejectsSharedLayout: a Fragment is a value in a tree, not a
// template handle, so a layout shared between two pages would have to hold both
// their content fragments in one single-fill slot. That is rejected at startup,
// with an error that says what to do about it.
func TestRegisterPage_RejectsSharedLayout(t *testing.T) {
	app := newTestApp(t, nil)
	shared := newLayout("shared-layout")

	first := newHomePage()
	first.LayoutFragment = shared
	if err := app.RegisterPage(first); err != nil {
		t.Fatalf("RegisterPage(first): %v", err)
	}

	second := newHomePage()
	second.Name = "about"
	second.LayoutFragment = shared
	second.ContentFragment = &types.Fragment{Name: "about-content", TemplatePath: "pages/about.html"}
	second.Paths = map[string]string{"en": "/about"}

	err := app.RegisterPage(second)
	if !errors.Is(err, types.ErrSlotOccupied) {
		t.Fatalf("RegisterPage(second) = %v, want ErrSlotOccupied", err)
	}
	for _, want := range []string{`page "about"`, "one per page"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
	if len(shared.Slots[types.DefaultContentSlot].Fill) != 1 {
		t.Fatal("the rejected registration left a second fill in the shared layout")
	}
}

// TestRegisterPage_RejectsMissingTemplate: a typo in a template path must be a
// startup error naming the fragment, not a 500 on the first request that reaches
// it. Both the layout and the content fragment are checked, and so is a fragment
// only reachable as a fallback.
func TestRegisterPage_RejectsMissingTemplate(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		mutate   func(*types.Page)
		fragment string
	}{
		{
			name:     "content",
			mutate:   func(p *types.Page) { p.ContentFragment.TemplatePath = "pages/typo.html" },
			fragment: "home-content",
		},
		{
			name:     "layout",
			mutate:   func(p *types.Page) { p.LayoutFragment.TemplatePath = "layouts/typo.html" },
			fragment: "layout",
		},
		{
			name: "fallback",
			mutate: func(p *types.Page) {
				p.ContentFragment.Fallback = &types.Fragment{
					Name:         "home-fallback",
					TemplatePath: "pages/typo.html",
				}
			},
			fragment: "home-fallback",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			app := newTestApp(t, nil)
			page := newHomePage()
			testCase.mutate(page)

			err := app.RegisterPage(page)
			if !errors.Is(err, ErrTemplateNotFound) {
				t.Fatalf("RegisterPage = %v, want ErrTemplateNotFound", err)
			}
			if !strings.Contains(err.Error(), testCase.fragment) {
				t.Fatalf("error %q does not name the fragment %q", err, testCase.fragment)
			}
			if !strings.Contains(err.Error(), `page "home"`) {
				t.Fatalf("error %q does not name the page", err)
			}
		})
	}
}

// TestRegisterPage_RejectsDuplicateName: two pages cannot share a name, since the
// name is how a plugin and the CLI reach a page.
func TestRegisterPage_RejectsDuplicateName(t *testing.T) {
	app := newTestApp(t, nil)
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	duplicate := newHomePage()
	duplicate.LayoutFragment = newLayout("other-layout")
	duplicate.Paths = map[string]string{"en": "/other"}

	err := app.RegisterPage(duplicate)
	if !errors.Is(err, ErrDuplicatePage) {
		t.Fatalf("RegisterPage = %v, want ErrDuplicatePage", err)
	}
	if !strings.Contains(err.Error(), "home") {
		t.Fatalf("error %q does not name the page", err)
	}
}

// TestRegisterPage_RejectsInvalidPage: every Page.Validate failure mode surfaces
// through RegisterPage with its own sentinel intact, named against the page.
func TestRegisterPage_RejectsInvalidPage(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		mutate   func(*types.Page)
		expected error
	}{
		{
			name:     "no name",
			mutate:   func(p *types.Page) { p.Name = "" },
			expected: types.ErrEmptyName,
		},
		{
			name:     "no content fragment",
			mutate:   func(p *types.Page) { p.ContentFragment = nil },
			expected: types.ErrMissingContent,
		},
		{
			name:     "path without a leading slash",
			mutate:   func(p *types.Page) { p.Paths = map[string]string{"en": "home"} },
			expected: types.ErrInvalidPath,
		},
		{
			name:     "incremental without a ttl",
			mutate:   func(p *types.Page) { p.CacheTTL = 0 },
			expected: types.ErrMissingTTL,
		},
		{
			name:     "negative ttl",
			mutate:   func(p *types.Page) { p.CacheTTL = -time.Second },
			expected: types.ErrInvalidTTL,
		},
		{
			name:     "self referential error page",
			mutate:   func(p *types.Page) { p.ErrorPage = p },
			expected: types.ErrSelfErrorPage,
		},
		{
			name:     "invalid redirect",
			mutate:   func(p *types.Page) { p.Redirects = []*types.Redirect{{From: "old", To: "/"}} },
			expected: types.ErrInvalidPath,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			app := newTestApp(t, nil)
			page := newHomePage()
			testCase.mutate(page)

			if err := app.RegisterPage(page); !errors.Is(err, testCase.expected) {
				t.Fatalf("RegisterPage = %v, want %v", err, testCase.expected)
			}
		})
	}
}

// TestRegisterPage_RejectsNil: a nil page has nothing to validate and nothing to
// name, so it is rejected on its own terms rather than by panicking.
func TestRegisterPage_RejectsNil(t *testing.T) {
	app := newTestApp(t, nil)
	for name, register := range map[string]func(*types.Page) error{
		"page":      app.RegisterPage,
		"not found": app.RegisterNotFoundPage,
		"error":     app.RegisterErrorPage,
	} {
		t.Run(name, func(t *testing.T) {
			if err := register(nil); !errors.Is(err, ErrNilPage) {
				t.Fatalf("register(nil) = %v, want ErrNilPage", err)
			}
		})
	}
}

// TestRegisterPage_RejectsLayoutWithoutContentSlot: a layout that never declares
// the content slot cannot hold a page's content, and must say so at startup.
func TestRegisterPage_RejectsLayoutWithoutContentSlot(t *testing.T) {
	app := newTestApp(t, nil)
	page := newHomePage()
	page.LayoutFragment.Slots = nil

	err := app.RegisterPage(page)
	if !errors.Is(err, types.ErrUnknownSlot) {
		t.Fatalf("RegisterPage = %v, want ErrUnknownSlot", err)
	}
}

// TestRegisterPage_RejectsDuplicateRoute: two pages cannot answer the same path in
// the same locale, and the router's own sentinel survives the trip.
func TestRegisterPage_RejectsDuplicateRoute(t *testing.T) {
	app := newTestApp(t, nil)
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	clash := newHomePage()
	clash.Name = "home-again"
	clash.LayoutFragment = newLayout("another-layout")

	if err := app.RegisterPage(clash); err == nil {
		t.Fatal("registering a second page at the same path = nil, want an error")
	}
	if _, ok := app.Page("home-again"); ok {
		t.Fatal("a page rejected by the router was still recorded in the registry")
	}
}

// ---------------------------------------------------------------------------
// Registration closes at startup
// ---------------------------------------------------------------------------

// TestRegistration_ClosesOnceStarted: everything registered after the handler is
// built would be invisible to the plugins that already ran Init, so it is
// rejected rather than silently ignored.
func TestRegistration_ClosesOnceStarted(t *testing.T) {
	app := newTestApp(t, nil)
	app.Handler()

	page := newHomePage()
	if err := app.RegisterPage(page); !errors.Is(err, ErrAppStarted) {
		t.Fatalf("RegisterPage after start = %v, want ErrAppStarted", err)
	}
	if err := app.RegisterNotFoundPage(page); !errors.Is(err, ErrAppStarted) {
		t.Fatalf("RegisterNotFoundPage after start = %v, want ErrAppStarted", err)
	}
	if err := app.RegisterErrorPage(page); !errors.Is(err, ErrAppStarted) {
		t.Fatalf("RegisterErrorPage after start = %v, want ErrAppStarted", err)
	}
	if err := app.RegisterPlugin(&lifecyclePlugin{}); !errors.Is(err, ErrAppStarted) {
		t.Fatalf("RegisterPlugin after start = %v, want ErrAppStarted", err)
	}
}

// TestRegisterCommand_StaysOpenAfterStart: plugins contribute commands from
// inside Init, which by definition runs after the application has started, so
// RegisterCommand must not be closed by ErrAppStarted.
func TestRegisterCommand_StaysOpenAfterStart(t *testing.T) {
	app := newTestApp(t, nil)
	if err := app.RegisterPlugin(&commandPlugin{}); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	app.Handler()

	commands := app.Commands()
	if len(commands) != 1 || commands[0].Name != "seed" {
		t.Fatalf("Commands = %v, want the one the plugin registered in Init", commands)
	}
}

// ---------------------------------------------------------------------------
// Not-found and error pages
// ---------------------------------------------------------------------------

// TestRegisterNotFoundPage serves an unmatched path and expects the registered
// page's own markup, which is what proves the router got it.
func TestRegisterNotFoundPage(t *testing.T) {
	app := newTestApp(t, nil)
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	notFound := &types.Page{
		Name:            "global-404",
		ContentFragment: &types.Fragment{Name: "404-content", TemplatePath: "pages/about.html"},
	}
	if err := app.RegisterNotFoundPage(notFound); err != nil {
		t.Fatalf("RegisterNotFoundPage: %v", err)
	}

	recorder := get(app.Handler(), "/nowhere")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if !strings.Contains(recorder.Body.String(), "About Us") {
		t.Fatalf("body = %q, want the registered not-found page", recorder.Body.String())
	}
	if _, ok := app.Page("global-404"); !ok {
		t.Fatal("the registered not-found page is missing from the page registry")
	}
}

// TestRegisterNotFoundPage_ValidatesLikeAnyOtherPage: the global error pages go
// through the same gate, so a typo in one is caught at startup too.
func TestRegisterNotFoundPage_ValidatesLikeAnyOtherPage(t *testing.T) {
	app := newTestApp(t, nil)
	page := &types.Page{
		Name:            "global-404",
		ContentFragment: &types.Fragment{Name: "404-content", TemplatePath: "pages/typo.html"},
	}
	if err := app.RegisterNotFoundPage(page); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("RegisterNotFoundPage = %v, want ErrTemplateNotFound", err)
	}
	if err := app.RegisterErrorPage(page); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("RegisterErrorPage = %v, want ErrTemplateNotFound", err)
	}
}

// TestRegisterErrorPage_AcceptsAnAlreadyRegisteredPage: the specification's own
// example registers a page with RegisterPage and then designates it, so naming
// the same page twice must not read as a duplicate of itself — nor bind its
// content into its layout a second time.
func TestRegisterErrorPage_AcceptsAnAlreadyRegisteredPage(t *testing.T) {
	app := newTestApp(t, nil)
	page := newHomePage()

	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.RegisterErrorPage(page); err != nil {
		t.Fatalf("RegisterErrorPage: %v", err)
	}

	if got := len(page.LayoutFragment.Slots[types.DefaultContentSlot].Fill); got != 1 {
		t.Fatalf("content slot has %d fills, want 1", got)
	}
	if got := len(app.Pages()); got != 1 {
		t.Fatalf("Pages = %d entries, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Host surface
// ---------------------------------------------------------------------------

// TestApp_PagesAreDefensiveCopies is the contract plugin.Host places on Pages and
// Page: a plugin holding a returned page must not be able to write through it into
// live framework state. Every container is mutated on the copy — including a
// Redirect reached through a shared element pointer, which copying only the slice
// would have left exposed.
func TestApp_PagesAreDefensiveCopies(t *testing.T) {
	app := newTestApp(t, nil)

	page := newHomePage()
	page.Redirects = []*types.Redirect{{From: "/old", To: "/", Permanent: true}}
	// Page.SEO is a map[string]any, and this test may not spell that type.
	// RenderContext.SharedData is the same type, so borrowing one is how a
	// populated SEO map is built here without declaring the forbidden type.
	page.SEO = types.NewRenderContext(context.Background(), nil, nil, "", nil).SharedData
	page.SEO["title"] = "Original"

	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	copied, ok := app.Page("home")
	if !ok {
		t.Fatal("Page(home) not found")
	}
	if copied == page {
		t.Fatal("Page returned the live page, want a copy")
	}

	copied.Name = "mutated"
	copied.Paths["en"] = "/mutated"
	copied.Paths["tr"] = "/mutated"
	copied.SEO["title"] = "Mutated"
	copied.DependencyTags[0] = "mutated"
	copied.Strategy = types.StrategyDynamic
	copied.Redirects[0].To = "/mutated"
	copied.Redirects[0].StatusCode = 307

	if page.Name != "home" {
		t.Fatalf("live page name = %q, want home", page.Name)
	}
	if page.Paths["en"] != "/" {
		t.Fatalf("live page path = %q, want /", page.Paths["en"])
	}
	if _, added := page.Paths["tr"]; added {
		t.Fatal("a path added to the copy reached the live page")
	}
	if page.SEO["title"] != "Original" {
		t.Fatalf("live page SEO title = %v, want Original", page.SEO["title"])
	}
	if page.DependencyTags[0] != "homepage" {
		t.Fatalf("live page tag = %q, want homepage", page.DependencyTags[0])
	}
	if page.Strategy != types.StrategyIncremental {
		t.Fatalf("live page strategy = %v, want incremental", page.Strategy)
	}
	if page.Redirects[0].To != "/" || page.Redirects[0].StatusCode != 0 {
		t.Fatalf("live redirect = %+v, want it untouched by the copy", *page.Redirects[0])
	}
}

// TestApp_PagesPreservesRegistrationOrder: Pages is what a plugin and the CLI
// enumerate, so it must not depend on map iteration order.
func TestApp_PagesPreservesRegistrationOrder(t *testing.T) {
	app := newTestApp(t, nil)

	home := newHomePage()
	about := &types.Page{
		Name:            "about",
		LayoutFragment:  newLayout("about-layout"),
		ContentFragment: &types.Fragment{Name: "about-content", TemplatePath: "pages/about.html"},
		Paths:           map[string]string{"en": "/about"},
	}
	for _, page := range []*types.Page{home, about} {
		if err := app.RegisterPage(page); err != nil {
			t.Fatalf("RegisterPage(%s): %v", page.Name, err)
		}
	}

	pages := app.Pages()
	if len(pages) != 2 {
		t.Fatalf("Pages = %d entries, want 2", len(pages))
	}
	if pages[0].Name != "home" || pages[1].Name != "about" {
		t.Fatalf("Pages order = %q, %q, want home, about", pages[0].Name, pages[1].Name)
	}
}

// TestApp_RegisterCommand covers the two ways a command is rejected and the copy
// Commands returns.
func TestApp_RegisterCommand(t *testing.T) {
	app := newTestApp(t, nil)

	if err := app.RegisterCommand(plugin.Command{Name: ""}); !errors.Is(err, ErrEmptyCommandName) {
		t.Fatalf("RegisterCommand with no name = %v, want ErrEmptyCommandName", err)
	}
	if err := app.RegisterCommand(plugin.Command{Name: "seed"}); err != nil {
		t.Fatalf("RegisterCommand: %v", err)
	}
	err := app.RegisterCommand(plugin.Command{Name: "seed"})
	if !errors.Is(err, ErrDuplicateCommand) {
		t.Fatalf("RegisterCommand with a duplicate name = %v, want ErrDuplicateCommand", err)
	}
	if !strings.Contains(err.Error(), "seed") {
		t.Fatalf("error %q does not name the command", err)
	}

	commands := app.Commands()
	commands[0].Name = "mutated"
	if app.Commands()[0].Name != "seed" {
		t.Fatal("mutating the slice Commands returned changed the application's own")
	}
}

// TestApp_RegisterPluginRejections: the plugin registry's own sentinels must not
// be swallowed on the way through.
func TestApp_RegisterPluginRejections(t *testing.T) {
	app := newTestApp(t, nil)

	if err := app.RegisterPlugin(nil); !errors.Is(err, plugin.ErrNilPlugin) {
		t.Fatalf("RegisterPlugin(nil) = %v, want ErrNilPlugin", err)
	}
	if err := app.RegisterPlugin(&lifecyclePlugin{}); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	if err := app.RegisterPlugin(&lifecyclePlugin{}); !errors.Is(err, plugin.ErrDuplicatePlugin) {
		t.Fatalf("RegisterPlugin(duplicate) = %v, want ErrDuplicatePlugin", err)
	}
}

// commandPlugin registers a CLI command from inside Init, the way a real plugin
// contributing a command does.
type commandPlugin struct{}

var _ plugin.Plugin = (*commandPlugin)(nil)

// Name identifies the plugin.
func (p *commandPlugin) Name() string { return "command" }

// Version reports the plugin's version.
func (p *commandPlugin) Version() string { return "1.0.0" }

// Init registers the plugin's command with the host.
func (p *commandPlugin) Init(_ context.Context, host plugin.Host) error {
	return host.RegisterCommand(plugin.Command{
		Name:  "seed",
		Usage: "seed",
		Short: "seed the database",
		Run:   func(context.Context, []string) error { return nil },
	})
}

// Shutdown does nothing.
func (p *commandPlugin) Shutdown(context.Context) error { return nil }

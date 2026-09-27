# Layout Chains and Guards Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pages declare an ordered layout chain (`WithLayouts`) and layouts carry guards (`WithGuard`) that the framework dispatches after routing and before cache — the Next.js private-layout model, with the policy left to plugins.

**Architecture:** The builder stores the chain on a new `Page.LayoutChain`; registration folds content into the innermost layout and wraps each layout into the next outer one (generalising `bindContent`). `Fragment.Guard` is collected from the spine (chain + content fragment) and run in `httpx` for page renders and page-attached actions; fragment paths run their own guard inside the action handler that already serves them. `WithLayout` and `InspectedPage.Layout` are removed — pre-1.0, no compatibility kept.

**Tech Stack:** Go stdlib only (framework has no external dependencies); tests use `testing/fstest` + `net/http/httptest`.

**Spec:** `docs/superpowers/specs/2026-09-27-layout-chains-and-guards-design.md`

## Global Constraints

- Pre-1.0: no backward compatibility. `WithLayout` is deleted, `InspectedPage.Layout` is replaced by `Layouts`.
- No new `any` in public API (project rule: never use type `any`).
- New error sentinels live in `internal/types/errors.go` and are re-exported as vars in `pkg/collage/types.go`.
- Builder methods record errors and keep returning the builder; errors surface via `BuildErr` and registration refuses a page carrying one (house pattern, see package doc in `pkg/collage/types.go`).
- Comments explain *why*, present tense, wrapped near 80 columns (house style — read neighbouring code before writing).
- Commit messages: conventional commits (`feat:`, `docs:`, `test:`), ending with `Co-Authored-By: Claude Code <noreply@anthropic.com>`.
- Every task ends with the full suite green: `go test ./...` and `go vet ./...`.

## Review Focus

The spec implies these failure modes but no listed test exercises them; each line's test is folded into the owning task below.

1. **Fragment-path leak via malformed decision:** a guard returning `Status: 302` with an empty `Location` falls through `writeActionResult`'s `Location != ""` switch into the `Fragment` branch and renders the fragment it was meant to protect. → Task 6 pins `Validate` at the fragment-path handler.
2. **Cached private page served to a blocked reader:** if the guard ran after the cache lookup, a logged-out second request would get the cached 200. → Task 5 pins guard-before-cache with an allowed render followed by a blocked request to the same cached page.
3. **Private page's form POST reachable logged-out:** a page-attached action inherits nothing unless `resolve` hands the handler the owning page. → Task 6 pins the blocked POST.
4. **Outer layout with a pre-filled content slot double-renders silently:** folding must refuse, not append. → Task 3 pins the registration error.
5. **Non-refusal decision written as a blank success:** a decision with a 2xx/3xx status and no `Location` must not become an empty 200. → Task 1 pins `GuardDecision.Validate`; Task 5 pins the 500 at dispatch.

---

### Task 1: Guard types on Fragment

**Files:**
- Create: `internal/types/guard.go`
- Modify: `internal/types/errors.go` (append two sentinels)
- Modify: `internal/types/fragment.go` (add `Guard` field after `Title`)
- Modify: `pkg/collage/fragment.go` (add `WithGuard` after `WithTitle`)
- Modify: `pkg/collage/types.go` (re-export types + sentinel)
- Test: `internal/types/guard_test.go`, `pkg/collage/fragment_test.go`

**Interfaces:**
- Consumes: existing `Fragment`, `FragmentBuilder`.
- Produces: `types.GuardFunc func(ctx context.Context, r *http.Request) (*GuardDecision, error)`; `types.GuardDecision struct { Status int; Location string }` with `func (d *GuardDecision) Validate() error`; `types.ErrInvalidGuardDecision`; `Fragment.Guard GuardFunc`; `FragmentBuilder.WithGuard(g GuardFunc) *FragmentBuilder`; `collage.GuardFunc` / `collage.GuardDecision` / `collage.ErrInvalidGuardDecision` aliases. Tasks 5 and 6 dispatch these.

- [ ] **Step 1: Write the failing tests**

`internal/types/guard_test.go`:

```go
package types

import (
	"errors"
	"net/http"
	"testing"
)

func TestGuardDecisionValidate(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		loc     string
		wantErr bool
	}{
		{"redirect with explicit status", http.StatusSeeOther, "/login", false},
		{"redirect with zero status means 303", 0, "/login", false},
		{"permanent redirect", http.StatusMovedPermanently, "/login", false},
		{"bare refusal", http.StatusUnauthorized, "", false},
		{"forbidden", http.StatusForbidden, "", false},
		{"redirect without location", http.StatusSeeOther, "", true},
		{"success status with no location", http.StatusOK, "", true},
		{"no decision content at all", 0, "", true},
		{"redirect status paired with location but out of range", http.StatusMovedPermanently + 200, "/login", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &GuardDecision{Status: tc.status, Location: tc.loc}
			err := d.Validate()
			if tc.wantErr && !errors.Is(err, ErrInvalidGuardDecision) {
				t.Fatalf("Validate() = %v, want ErrInvalidGuardDecision", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}
```

Append to `pkg/collage/fragment_test.go` (match the file's existing import block; add `"net/http"` if absent):

```go
func TestWithGuard(t *testing.T) {
	guard := func(ctx context.Context, r *http.Request) (*GuardDecision, error) {
		return &GuardDecision{Status: http.StatusSeeOther, Location: "/login"}, nil
	}
	f := NewFragment("private", "layouts/private.html").WithGuard(guard).Build()
	if f.Guard == nil {
		t.Fatal("WithGuard left Fragment.Guard nil")
	}
	d, err := f.Guard(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || d == nil || d.Location != "/login" {
		t.Fatalf("Guard() = %v, %v; want /login, nil", d, err)
	}
}

func TestWithGuardNil(t *testing.T) {
	b := NewFragment("private", "layouts/private.html").WithGuard(nil)
	if err := b.BuildErr(); err == nil {
		t.Fatal("WithGuard(nil) recorded no error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/types/ -run TestGuardDecisionValidate -v && go test ./pkg/collage/ -run 'TestWithGuard' -v`
Expected: FAIL — `GuardDecision` undefined, `WithGuard` undefined.

- [ ] **Step 3: Write the implementation**

`internal/types/guard.go`:

```go
package types

import (
	"context"
	"fmt"
	"net/http"
)

// GuardFunc decides whether a request may reach what it guards. It runs after
// the router has resolved the request and before anything is served or read
// from cache, so a blocked reader never touches the page's render or its cache
// entry.
//
// A nil decision with a nil error allows the request. A non-nil decision
// blocks it: a redirect — a 3xx Status with a Location — or a bare refusal
// status such as 401 or 403. An error fails the request; it is not a refusal,
// and it is not an allow.
//
// What a guard checks is not the framework's to know. The policy — whether a
// session holds a user, whether a reader is a member of the team — lives in
// the application or a plugin; see WithGuard for how a layout carries one.
type GuardFunc func(ctx context.Context, r *http.Request) (*GuardDecision, error)

// GuardDecision is a guard's answer about one request.
type GuardDecision struct {
	// Status is the HTTP status to answer with. Zero with a Location means 303
	// See Other, which turns a form's POST into the GET that follows it; zero
	// without a Location is not a decision anything can write, and Validate
	// rejects it.
	Status int
	// Location is the redirect target when Status is 3xx. It is written into
	// the Location header as-is: an absolute path, or whatever the policy
	// considers right to send this reader to.
	Location string
}

// Validate reports whether d is a decision the framework can write: a 3xx
// status (explicitly, or zero, meaning 303) paired with a non-empty Location,
// or a refusal status in 400-599 with no Location. Anything else — a success
// status, a redirect with nowhere to go, an empty decision — would arrive at
// the reader as a blank response or fall through to the very content the
// guard exists to keep from it, so it is refused rather than guessed at.
func (d *GuardDecision) Validate() error {
	if d.Location != "" {
		status := d.Status
		if status == 0 {
			status = http.StatusSeeOther
		}
		if status >= 300 && status <= 399 {
			return nil
		}
		return fmt.Errorf("%w: status %d with a location must be a redirect", ErrInvalidGuardDecision, d.Status)
	}
	if d.Status >= 400 && d.Status <= 599 {
		return nil
	}
	return fmt.Errorf("%w: status %d with no location must be a refusal in 400-599", ErrInvalidGuardDecision, d.Status)
}
```

Append to `internal/types/errors.go`:

```go
// ErrInvalidGuardDecision is returned when a guard's decision cannot be
// written: a redirect status without a location, a success status, or an empty
// decision. A guard that cannot say what it means is failed rather than
// guessed about — the alternative, for a redirect with nowhere to go, is
// serving the content the guard was put there to keep from the reader.
var ErrInvalidGuardDecision = errors.New("collage: invalid guard decision")
```

In `internal/types/fragment.go`, add after the `Title` field:

```go
	// Guard, when set, is asked whether a request may reach this fragment
	// before anything is served: before the page's cache is read, before its
	// render, before an action on the page's own URL runs. It runs for every
	// page whose spine this fragment is on — a layout in the page's layout
	// chain, or the page's content fragment — and for a fragment path opened
	// on this fragment's own URL, where it is the fragment's whole policy.
	// On any other fragment it is ignored: access policy belongs to routes,
	// not to rendering parts. See GuardFunc.
	Guard GuardFunc
```

In `pkg/collage/fragment.go`, add after `WithTitle`:

```go
// WithGuard sets the guard this fragment carries. A layout with a guard makes
// every page it wraps guarded — the page's renders and the actions on its own
// URL alike — which is how a section of a site says it is private:
//
//	private := collage.NewFragment("private", "layouts/private.html").
//		WithGuard(session.RequireUser("/login")).
//		Build()
//
//	collage.NewPage("dashboard").
//		WithLayouts(layouts.Master(), private).
//		WithContent(dashboard).
//		WithPath("en", "/dashboard")
//
// The guard runs after routing resolves the request and before the page's
// cache is read, so a blocked reader never reaches a cached render of what it
// is blocked from. A nil g records an error retrievable via BuildErr.
func (b *FragmentBuilder) WithGuard(g GuardFunc) *FragmentBuilder {
	if g == nil {
		b.errs = append(b.errs, fmt.Errorf("collage: nil guard on fragment %q", b.fragment.Name))
		return b
	}
	b.fragment.Guard = g
	return b
}
```

In `pkg/collage/types.go`, add near the `DataHandlerFunc` alias:

```go
// GuardFunc decides whether a request may reach what it guards. See
// FragmentBuilder.WithGuard.
type GuardFunc = types.GuardFunc

// GuardDecision is a guard's answer about one request: a redirect (3xx status
// with a location) or a bare refusal status.
type GuardDecision = types.GuardDecision
```

and near the other `Err` vars:

```go
// ErrInvalidGuardDecision is returned when a guard's decision cannot be
// written as an answer.
var ErrInvalidGuardDecision = types.ErrInvalidGuardDecision
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/types/ ./pkg/collage/`
Expected: PASS (all existing tests too).

- [ ] **Step 5: Commit**

```bash
git add internal/types/guard.go internal/types/guard_test.go internal/types/errors.go internal/types/fragment.go pkg/collage/fragment.go pkg/collage/fragment_test.go pkg/collage/types.go
git commit -m "feat: GuardFunc and GuardDecision, a fragment's WithGuard"
```

---

### Task 2: WithLayouts and Page.LayoutChain

`WithLayout` keeps working during this task by writing the same field; Task 4 removes it and migrates call sites.

**Files:**
- Modify: `internal/types/page.go` (add `LayoutChain` field after `LayoutFragment`; add `Guards()` method)
- Modify: `internal/types/errors.go` (append `ErrMissingLayout`, `ErrConflictingLayout`)
- Modify: `pkg/collage/page.go` (add `WithLayouts`; make `WithLayout` delegate)
- Modify: `pkg/collage/types.go` (re-export sentinels)
- Modify: `internal/types/builderr.go` (walk the chain)
- Test: `pkg/collage/page_test.go`, `internal/types/builderr_test.go` (create if absent)

**Interfaces:**
- Consumes: `Fragment` from Task 1 (untouched here except reading).
- Produces: `Page.LayoutChain []*Fragment` (outermost first, set by the builder, folded copies do not touch it); `Page.Guards() []GuardFunc` (chain order then content, nil-skipping); `PageBuilder.WithLayouts(outermost ...*Fragment) *PageBuilder`; `collage.ErrMissingLayout`, `collage.ErrConflictingLayout`. Tasks 3, 5, 6 and 7 consume these.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/collage/page_test.go` (imports already include `errors`, `testing`; ensure they do):

```go
// buildOK builds the page and fails the test when its builder recorded
// anything, so every test below reads one pattern.
func buildOK(t *testing.T, b *PageBuilder) *Page {
	t.Helper()
	p := b.Build()
	if err := b.BuildErr(); err != nil {
		t.Fatalf("BuildErr() = %v, want nil", err)
	}
	return p
}

func TestWithLayoutsStoresChain(t *testing.T) {
	outer := NewFragment("outer", "layouts/outer.html").Build()
	inner := NewFragment("inner", "layouts/inner.html").Build()
	content := NewFragment("home", "pages/home.html").Build()
	p := buildOK(t, NewPage("home").WithLayouts(outer, inner).WithContent(content).WithPath("en", "/"))
	if len(p.LayoutChain) != 2 || p.LayoutChain[0] != outer || p.LayoutChain[1] != inner {
		t.Fatalf("LayoutChain = %v, want [outer inner]", p.LayoutChain)
	}
}

func TestWithLayoutsEmpty(t *testing.T) {
	b := NewPage("home").WithLayouts().WithContent(NewFragment("home", "pages/home.html").Build())
	b.Build()
	if err := b.BuildErr(); err == nil {
		t.Fatal("WithLayouts() with no arguments recorded no error")
	} else if !errors.Is(err, ErrMissingLayout) {
		t.Fatalf("err = %v, want ErrMissingLayout", err)
	}
}

func TestWithLayoutsNilEntry(t *testing.T) {
	b := NewPage("home").WithLayouts(NewFragment("outer", "layouts/outer.html").Build(), nil).
		WithContent(NewFragment("home", "pages/home.html").Build())
	b.Build()
	if err := b.BuildErr(); err == nil {
		t.Fatal("WithLayouts with a nil entry recorded no error")
	} else if !errors.Is(err, ErrNilFragment) {
		t.Fatalf("err = %v, want ErrNilFragment", err)
	}
}

func TestWithLayoutsDuplicate(t *testing.T) {
	layout := NewFragment("outer", "layouts/outer.html").Build()
	b := NewPage("home").WithLayouts(layout, layout).
		WithContent(NewFragment("home", "pages/home.html").Build())
	b.Build()
	if err := b.BuildErr(); err == nil {
		t.Fatal("WithLayouts with a duplicate layout recorded no error")
	} else if !errors.Is(err, ErrFragmentCycle) {
		t.Fatalf("err = %v, want ErrFragmentCycle", err)
	}
}

func TestWithLayoutsTwice(t *testing.T) {
	first := NewFragment("a", "layouts/a.html").Build()
	second := NewFragment("b", "layouts/b.html").Build()
	b := NewPage("home").WithLayouts(first).WithLayouts(second).
		WithContent(NewFragment("home", "pages/home.html").Build())
	b.Build()
	if err := b.BuildErr(); err == nil {
		t.Fatal("a second WithLayouts recorded no error")
	} else if !errors.Is(err, ErrConflictingLayout) {
		t.Fatalf("err = %v, want ErrConflictingLayout", err)
	}
}

func TestPageGuardsOrder(t *testing.T) {
	var order []string
	g := func(name string) GuardFunc {
		return func(ctx context.Context, r *http.Request) (*GuardDecision, error) {
			order = append(order, name)
			return nil, nil
		}
	}
	outer := NewFragment("outer", "layouts/outer.html").WithGuard(g("outer")).Build()
	inner := NewFragment("inner", "layouts/inner.html").WithGuard(g("inner")).Build()
	content := NewFragment("home", "pages/home.html").WithGuard(g("content")).Build()
	middle := NewFragment("middle", "layouts/middle.html").Build() // no guard: skipped, not an error
	p := NewPage("home").WithLayouts(outer, middle, inner).WithContent(content).Build()
	guards := p.Guards()
	if len(guards) != 3 {
		t.Fatalf("Guards() has %d guards, want 3", len(guards))
	}
	p.Guards()[0](nil, nil)
	p.Guards()[1](nil, nil)
	p.Guards()[2](nil, nil)
	if len(order) != 3 || order[0] != "outer" || order[1] != "inner" || order[2] != "content" {
		t.Fatalf("guard order = %v, want [outer inner content]", order)
	}
}
```

`page_test.go` is `package collage` (internal-name tests) — check its package clause and keep the snippet's unqualified names consistent with it (`errors`, `net/http`, `net/http/httptest` added to imports as needed).

`internal/types/builderr_test.go` (create):

```go
package types

import "testing"

// A layout chain entry's builder error must reach PageBuildErr even though
// LayoutFragment is still nil before registration folds the chain.
func TestPageBuildErrWalksLayoutChain(t *testing.T) {
	bad := &Fragment{Name: "bad", TemplatePath: "x.html"}
	bad.setBuildErr(ErrNilFragment)
	p := &Page{
		Name:            "p",
		ContentFragment: &Fragment{Name: "c", TemplatePath: "c.html"},
		LayoutChain:     []*Fragment{&Fragment{Name: "ok", TemplatePath: "o.html"}, bad},
	}
	if err := PageBuildErr(p); err == nil {
		t.Fatal("PageBuildErr missed the chain entry's build error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/collage/ -run 'TestWithLayouts|TestPageGuards' -v && go test ./internal/types/ -run TestPageBuildErrWalksLayoutChain -v`
Expected: FAIL — `WithLayouts` undefined, `LayoutChain`/`Guards` undefined.

- [ ] **Step 3: Write the implementation**

In `internal/types/page.go`, add after `LayoutFragment`:

```go
	// LayoutChain holds the page's layouts outermost-first, as the builder
	// declared them. Registration folds the chain into LayoutFragment — a
	// per-page copy of each layout, the content fragment bound into the
	// innermost and each layout into the next outer one — and leaves this
	// field pointing at the originals, which is where a guard or an inspection
	// reads the spine from. Empty for a page with no layout.
	LayoutChain []*Fragment
```

and after `ContentSlotName`:

```go
// Guards returns the guards on the page's spine: each layout in LayoutChain,
// outermost first, then the content fragment. A nil guard — the common
// fragment — is skipped rather than represented. The first guard in this
// order is the first asked; see the httpx dispatch for what a decision does.
func (p *Page) Guards() []GuardFunc {
	var out []GuardFunc
	for _, f := range p.LayoutChain {
		if f != nil && f.Guard != nil {
			out = append(out, f.Guard)
		}
	}
	if p.ContentFragment != nil && p.ContentFragment.Guard != nil {
		out = append(out, p.ContentFragment.Guard)
	}
	return out
}
```

Append to `internal/types/errors.go`:

```go
// ErrMissingLayout is recorded when WithLayouts is called with no layouts at
// all. A page wanting no layout simply does not call it; a call that names
// none is a chain somebody forgot to finish.
var ErrMissingLayout = errors.New("collage: missing layout")

// ErrConflictingLayout is recorded when WithLayouts is called a second time on
// one builder. Whether the second call meant to replace or to extend the chain
// is not something the builder can tell, and guessing silently reorders what a
// page is wrapped in.
var ErrConflictingLayout = errors.New("collage: layout chain already declared")
```

In `pkg/collage/page.go`, replace `WithLayout` with the pair (keep the same doc spirit):

```go
// WithLayouts sets the page's layout chain, outermost first: the first
// fragment wraps the second, the last wraps the page's content. Registration
// does the wrapping — each layout's "content" slot is filled by the builder,
// never by hand:
//
//	collage.NewPage("login").
//		WithLayouts(layouts.Master(), layouts.Auth()).
//		WithContent(login).
//		WithPath("en", "/login")
//
// Every layout in the chain renders the one inside it through
// {{slot "content"}}, the same convention a single layout follows. Each page
// gets its own copy of every layout's slot table at registration, so one
// layout value can be shared by every page in the application.
//
// A nil entry records ErrNilFragment, the same fragment twice records
// ErrFragmentCycle, a second call records ErrConflictingLayout, and no layouts
// at all records ErrMissingLayout — each retrievable via BuildErr and refused
// at registration.
func (b *PageBuilder) WithLayouts(outermost ...*Fragment) *PageBuilder {
	return b.setLayouts(outermost)
}

// WithLayout sets a page's single layout: the one-fragment chain. See
// WithLayouts. [REMOVED IN TASK 4 — this method only bridges the migration.]
func (b *PageBuilder) WithLayout(f *Fragment) *PageBuilder {
	return b.setLayouts([]*Fragment{f})
}

func (b *PageBuilder) setLayouts(chain []*Fragment) *PageBuilder {
	if len(b.page.LayoutChain) > 0 {
		b.errs = append(b.errs, fmt.Errorf("%w: page %q", ErrConflictingLayout, b.page.Name))
		return b
	}
	if len(chain) == 0 {
		b.errs = append(b.errs, fmt.Errorf("%w: page %q declared no layouts", ErrMissingLayout, b.page.Name))
		return b
	}
	seen := make(map[*Fragment]bool, len(chain))
	for i, f := range chain {
		if f == nil {
			b.errs = append(b.errs, fmt.Errorf("%w: page %q layout %d", ErrNilFragment, b.page.Name, i))
			return b
		}
		if seen[f] {
			b.errs = append(b.errs, fmt.Errorf("%w: page %q wraps layout %q twice", ErrFragmentCycle, b.page.Name, f.Name))
			return b
		}
		seen[f] = true
	}
	b.page.LayoutChain = append([]*Fragment(nil), chain...)
	return b
}
```

Add `"fmt"` to `pkg/collage/page.go` imports if missing.

In `pkg/collage/types.go`, near the other sentinels:

```go
// ErrMissingLayout is recorded when WithLayouts is called with no layouts.
var ErrMissingLayout = types.ErrMissingLayout

// ErrConflictingLayout is recorded when WithLayouts is called a second time on
// one builder.
var ErrConflictingLayout = types.ErrConflictingLayout
```

In `internal/types/builderr.go`, replace `walk(p.LayoutFragment)` with:

```go
	// The chain, not the folded LayoutFragment: before registration folds the
	// chain, LayoutFragment is nil, and after it the chain originals carry the
	// same builder errors the copies in the folded tree do.
	for _, f := range p.LayoutChain {
		walk(f)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/types/ ./pkg/collage/ ./internal/core/`
Expected: PASS — including the existing single-layout tests, which now go through the same field. If an existing test asserted the old `WithLayout` + `LayoutFragment`-before-registration behaviour, fix the test to read `LayoutChain`; registration folding (Task 3) is what sets `LayoutFragment`.

- [ ] **Step 5: Commit**

```bash
git add internal/types/page.go internal/types/errors.go internal/types/builderr.go internal/types/builderr_test.go pkg/collage/page.go pkg/collage/page_test.go pkg/collage/types.go
git commit -m "feat: WithLayouts and Page.LayoutChain with spine Guards()"
```

---

### Task 3: Registration folds the chain

**Files:**
- Modify: `internal/core/registry.go:198-227` (`bindContent`) and the `contentBound` helper area
- Modify: `internal/core/registry.go:524` (`copyPage` — clone `LayoutChain`)
- Test: `internal/core/registry_test.go`

**Interfaces:**
- Consumes: `Page.LayoutChain` from Task 2; existing `copyLayout`, `types.DefaultContentSlot`, `a.bound`.
- Produces: the invariant every later task relies on — after `RegisterPage`, `p.LayoutFragment` is the folded root (copy of the outermost layout, with the folded chain inside), `p.LayoutChain` still holds the originals, and every page's slot tables are private.

- [ ] **Step 0: Migrate the two direct LayoutFragment assignments**

Two sites construct pages by setting `LayoutFragment` directly and will break when `bindContent` reads the chain instead — both are in `internal/core` tests:

- `internal/core/app_test.go:169` `newHomePage()`: change `LayoutFragment: newLayout("layout"),` to `LayoutChain: []*types.Fragment{newLayout("layout")},`. Every test using this helper keeps working: folding still sets `p.LayoutFragment` at registration, which is what the assertions read.
- `internal/core/registry_test.go:254` `TestRegisterPage_HandBoundLayoutStillGetsItsOwnCopy`: it sets `page.LayoutFragment = shared` after `newHomePage()`; change to `page.LayoutChain = []*types.Fragment{shared}` (drop the now-redundant `LayoutFragment` line). The hand-bound escape hatch is unchanged: the innermost layout already holding the content fragment in its content slot skips the Bind and still gets a private copy.

Then confirm nothing else does: `grep -rn "\.LayoutFragment = \|LayoutFragment:" --include="*.go" internal/ pkg/ | grep -v registry.go` — expected output: only `pkg/collage/page.go`'s builder path (removed in Task 2's flow) or nothing.

- [ ] **Step 1: Write the failing tests**

Append to `internal/core/registry_test.go`. The helpers this file already has: `newTestApp(t, nil)` builds an app over the default template set (`layouts/default.html`, `pages/home.html`), and `newLayout(name)` returns a layout fragment declaring the required content slot. Fragments below reuse those template paths so `checkTemplates` accepts them:

```go
// foldTree spells out the chain one page folded: master's content slot holds
// the auth copy, whose content slot holds the content fragment.
func foldTree(t *testing.T, p *types.Page) (master, auth *types.Fragment) {
	t.Helper()
	master = p.LayoutFragment
	if master == nil || master.Name != "master" {
		t.Fatalf("root = %v, want the folded master copy", master)
	}
	slot, ok := master.Slot(types.DefaultContentSlot)
	if !ok || len(slot.Fill) != 1 {
		t.Fatalf("master content slot = %v, want exactly one fill", slot)
	}
	auth = slot.Fill[0]
	if auth.Name != "auth" || auth == p.LayoutChain[1] {
		t.Fatalf("inner layout = %v, want a copy of auth", auth)
	}
	inner, ok := auth.Slot(types.DefaultContentSlot)
	if !ok || len(inner.Fill) != 1 || inner.Fill[0].Name != "content" {
		t.Fatalf("auth content slot = %v, want the content fragment", inner)
	}
	return master, auth
}

func newNamedFragment(name, templatePath string) *types.Fragment {
	return &types.Fragment{Name: name, TemplatePath: templatePath, Slots: map[string]*types.SlotDefinition{}}
}

func TestRegisterPageFoldsLayoutChain(t *testing.T) {
	app := newTestApp(t, nil)
	master := newLayout("master")
	auth := newLayout("auth")
	p := &types.Page{
		Name:            "login",
		LayoutChain:     []*types.Fragment{master, auth},
		ContentFragment: newNamedFragment("content", "pages/home.html"),
		Paths:           map[string]string{"en": "/login"},
	}
	if err := app.RegisterPage(p); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	foldTree(t, p)
	// The originals are untouched: registration binds into copies.
	if _, ok := master.Slot(types.DefaultContentSlot); ok {
		t.Fatal("registration bound into the shared master layout")
	}
}

func TestRegisterPageChainSlotsPrivatePerPage(t *testing.T) {
	app := newTestApp(t, nil)
	master := newLayout("master")
	for _, name := range []string{"a", "b", "c"} {
		p := &types.Page{
			Name:            name,
			LayoutChain:     []*types.Fragment{master},
			ContentFragment: newNamedFragment(name+"-content", "pages/home.html"),
			Paths:           map[string]string{"en": "/" + name},
		}
		if err := app.RegisterPage(p); err != nil {
			t.Fatalf("RegisterPage(%s): %v", name, err)
		}
	}
	// Three pages on one shared layout value, and the first one's slot table
	// stayed private: still exactly its own content in its content slot.
	pageA, ok := app.Page("a")
	if !ok {
		t.Fatal("page a not found")
	}
	slot, _ := pageA.LayoutFragment.Slot(types.DefaultContentSlot)
	if len(slot.Fill) != 1 || slot.Fill[0].Name != "a-content" {
		t.Fatalf("page a content slot = %v, want exactly a-content", slot.Fill)
	}
}

func TestRegisterPageOuterLayoutPrefilledRejected(t *testing.T) {
	app := newTestApp(t, nil)
	stray := newNamedFragment("stray", "pages/home.html")
	outer := newLayout("outer")
	if err := outer.Bind(types.DefaultContentSlot, stray); err != nil {
		t.Fatalf("hand-bind: %v", err)
	}
	p := &types.Page{
		Name:            "login",
		LayoutChain:     []*types.Fragment{outer, newLayout("inner")},
		ContentFragment: newNamedFragment("content", "pages/home.html"),
		Paths:           map[string]string{"en": "/login"},
	}
	err := app.RegisterPage(p)
	if err == nil {
		t.Fatal("a chain whose outer layout has a filled content slot was accepted")
	}
}

func TestRegisterPageInnermostHandBoundStillCopies(t *testing.T) {
	app := newTestApp(t, nil)
	content := newNamedFragment("content", "pages/home.html")
	inner := newLayout("inner")
	if err := inner.Bind(types.DefaultContentSlot, content); err != nil {
		t.Fatalf("hand-bind: %v", err)
	}
	p := &types.Page{
		Name:            "login",
		LayoutChain:     []*types.Fragment{inner},
		ContentFragment: content,
		Paths:           map[string]string{"en": "/login"},
	}
	if err := app.RegisterPage(p); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if p.LayoutFragment == inner {
		t.Fatal("hand-bound innermost layout was not copied")
	}
	slot, _ := p.LayoutFragment.Slot(types.DefaultContentSlot)
	if len(slot.Fill) != 1 || slot.Fill[0] != content {
		t.Fatalf("hand-bound fill = %v, want the original content once", slot.Fill)
	}
}
```

(`newLayout` declares its content slot `Required` and single-fill — exactly the shape the folding tests exercise: a required slot is satisfied by the fold, and a second fill would error rather than append.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/core/ -run 'TestRegisterPage(FoldsLayoutChain|ChainSlotsPrivate|OuterLayoutPrefilled|InnermostHandBound)' -v`
Expected: FAIL — `bindContent` still reads `p.LayoutFragment` (nil for chain-built pages), so nothing folds.

- [ ] **Step 3: Write the implementation**

Replace `bindContent` in `internal/core/registry.go` (keep its doc comment, extended):

```go
// bindContent folds p's layout chain into p.LayoutFragment: each layout gets
// its own per-page copy of its slot table, the content fragment is bound into
// the innermost copy's content slot, and each copy is bound into the next
// outer copy's content slot. p.LayoutFragment ends as the outermost copy — the
// folded tree's root — and p.LayoutChain keeps pointing at the originals.
//
// The per-page copies are what make layouts shareable; see copyLayout. Only
// the innermost layout may arrive with its content slot already holding the
// content fragment — the hand-bound escape hatch — and no layout may arrive
// with any other fill in its content slot: registration fills every content
// slot in the chain itself, so one that is already filled is a page whose
// author and whose registration disagree about what it wraps.
//
// It must be called with a.mu held.
func (a *App) bindContent(p *types.Page) error {
	if len(p.LayoutChain) == 0 {
		return nil
	}
	if p.ContentFragment == nil {
		return fmt.Errorf("collage: page %q: %w", p.Name, types.ErrMissingContent)
	}
	if a.bound[p] {
		return nil
	}

	copies := make([]*types.Fragment, len(p.LayoutChain))
	for i, layout := range p.LayoutChain {
		copies[i] = copyLayout(layout)
	}

	innermost := copies[len(copies)-1]
	if !contentBound(innermost, p.ContentFragment) {
		if err := requireEmptyContentSlot(p, innermost); err != nil {
			return err
		}
		if err := innermost.Bind(types.DefaultContentSlot, p.ContentFragment); err != nil {
			return fmt.Errorf(
				"collage: page %q: binding content fragment %q into layout fragment %q slot %q: %w",
				p.Name, p.ContentFragment.Name, p.LayoutChain[len(p.LayoutChain)-1].Name, types.DefaultContentSlot, err,
			)
		}
	}
	for i := len(copies) - 2; i >= 0; i-- {
		if err := requireEmptyContentSlot(p, copies[i]); err != nil {
			return err
		}
		if err := copies[i].Bind(types.DefaultContentSlot, copies[i+1]); err != nil {
			return fmt.Errorf(
				"collage: page %q: binding layout %q into layout %q slot %q (registration fills every content slot in the chain, so no layout may have it filled already): %w",
				p.Name, p.LayoutChain[i+1].Name, p.LayoutChain[i].Name, types.DefaultContentSlot, err,
			)
		}
	}

	p.LayoutFragment = copies[0]
	a.bound[p] = true
	return nil
}

// requireEmptyContentSlot rejects a layout whose content slot already holds
// any fragment. The innermost layout's hand-bound escape hatch is checked by
// contentBound before this runs; anything else in a content slot is a fill
// registration never made and cannot account for.
func requireEmptyContentSlot(p *types.Page, layout *types.Fragment) error {
	if slot, ok := layout.Slot(types.DefaultContentSlot); ok && len(slot.Fill) > 0 {
		return fmt.Errorf(
			"collage: page %q: layout %q content slot is already filled (registration fills the content slot itself, so a layout must not have it filled already)",
			p.Name, layout.Name,
		)
	}
	return nil
}
```

Also update the `bindContent` doc comment block above the old code (lines 167-197) to describe the chain; delete prose that describes single-layout binding where the new comment covers it.

In `copyPage` (registry.go:524), after `copied.DependencyTags = ...`:

```go
	copied.LayoutChain = slices.Clone(p.LayoutChain)
```

and extend its doc comment's shared-by-pointer list to mention that the chain's fragment pointers stay shared (one level, like Redirects).

Check `internal/core/app.go:780` `pageReady`: its `p.LayoutFragment == nil` test still holds post-fold (a chain page has LayoutFragment set exactly once bound). Leave it, and leave `inspect.go` for Task 7.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS. The existing `TestRegisterPage_HandBoundLayoutStillGetsItsOwnCopy` must still pass — it goes through the innermost hand-bound path. `TestRegisterPage_LayoutContentSlotFilled` (if present under another name — grep `must not have it filled` in tests) may assert the old error text; update its expectation to the new message shape if so.

- [ ] **Step 5: Commit**

```bash
git add internal/core/registry.go internal/core/registry_test.go
git commit -m "feat: registration folds a page's layout chain"
```

---

### Task 4: Remove WithLayout, migrate every call site

**Files:**
- Modify: `pkg/collage/page.go` (delete `WithLayout`)
- Modify: every test using it: `pkg/collage/registered_test.go`, `collage_test.go`, `http_test.go`, `url_test.go`, `staticparams_test.go`, `build_test.go`, `extend_test.go`, `page_test.go`, `resolver_test.go`, `head_test.go`, `strategy_test.go` (grep to confirm the full list, including `internal/`)
- Modify: code samples in `README.md`, `docs/fragments.md`, `docs/routing.md`, `docs/caching.md`, `docs/actions.md`, `docs/cli.md`

**Interfaces:**
- Consumes: `WithLayouts` from Task 2.
- Produces: the public surface this feature ships with — `WithLayouts` only.

- [ ] **Step 1: Enumerate the call sites**

Run: `grep -rn "WithLayout(" --include="*.go" . | grep -v WithLayouts` and `grep -rn "WithLayout" README.md docs/*.md`
Note: `WithLayouts(` also matches `WithLayout(` under `grep "WithLayout("` — filter lines whose call is followed by `s`. The list above is from the same grep at planning time; re-run to catch strays.

- [ ] **Step 2: Migrate mechanically**

Each `.WithLayout(x)` becomes `.WithLayouts(x)`. In prose (docs, README), rewrite sentences that name `WithLayout` to name `WithLayouts`. No behaviour change is intended anywhere in this task; if a migration changes what a test asserts, stop and re-read the test — it was relying on something Task 2/3 should have preserved.

Then delete from `pkg/collage/page.go`:

```go
// WithLayout sets a page's single layout: the one-fragment chain. See
// WithLayouts. [REMOVED IN TASK 4 — this method only bridges the migration.]
func (b *PageBuilder) WithLayout(f *Fragment) *PageBuilder {
	return b.setLayouts([]*Fragment{f})
}
```

- [ ] **Step 3: Verify nothing references it and the suite is green**

Run: `grep -rn "WithLayout(" --include="*.go" . | grep -v WithLayouts; go test ./... && go vet ./...`
Expected: grep prints nothing; tests and vet pass.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "feat!: WithLayouts replaces WithLayout (pre-1.0, no compat)"
```

---

### Task 5: Guard dispatch for page renders

**Files:**
- Create: `internal/httpx/guard.go`
- Modify: `internal/httpx/handler.go` (add `stageGuard` constant near line 89; insert the dispatch after `route.at(pagePattern, match.Locale)` at ~line 605, before the `PageResolved` dispatch at ~line 607)
- Modify: `internal/httpx/action.go` (`writeActionResult`'s Location branch calls the extracted `writeRedirect`)
- Test: `pkg/collage/guard_test.go` (create), `internal/httpx/guard_test.go` (create)

**Interfaces:**
- Consumes: `Page.Guards()` (Task 2), `GuardDecision.Validate` (Task 1), `serveFailure`/`failure` (httpx).
- Produces: `runGuards(ctx, r, guards) (*types.GuardDecision, error)`; `(h *Handler) writeGuardAnswer(w, r, d) (int, error)`; `writeRedirect(w, r, status, location) int` (module-private, honors `FetchHeader`); `stageGuard = "guard"`. Task 6 reuses all of these.

- [ ] **Step 1: Write the failing tests**

`internal/httpx/guard_test.go`:

```go
package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

func TestRunGuardsShortCircuits(t *testing.T) {
	var asked []string
	g := func(name string, d *types.GuardDecision) types.GuardFunc {
		return func(ctx context.Context, r *http.Request) (*types.GuardDecision, error) {
			asked = append(asked, name)
			return d, nil
		}
	}
	d, err := runGuards(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil), []types.GuardFunc{
		g("first", nil),
		g("second", &types.GuardDecision{Status: http.StatusSeeOther, Location: "/login"}),
		g("third", nil),
	})
	if err != nil || d == nil || d.Location != "/login" {
		t.Fatalf("runGuards = %v, %v; want the second's decision", d, err)
	}
	if len(asked) != 2 {
		t.Fatalf("asked = %v, want first and second only", asked)
	}
}

func TestRunGuardsStopsOnError(t *testing.T) {
	var asked int
	failing := func(ctx context.Context, r *http.Request) (*types.GuardDecision, error) {
		asked++
		return nil, errors.New("session store down")
	}
	after := func(ctx context.Context, r *http.Request) (*types.GuardDecision, error) {
		t.Fatal("a guard after a failing guard ran")
		return nil, nil
	}
	if _, err := runGuards(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil), []types.GuardFunc{failing, after}); err == nil {
		t.Fatal("runGuards swallowed the error")
	}
	if asked != 1 {
		t.Fatalf("asked %d guards, want 1", asked)
	}
}

func TestWriteRedirectFetchConvention(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set(FetchHeader, "1")
	status := writeRedirect(w, r, http.StatusSeeOther, "/login")
	if status != http.StatusNoContent || w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 for a fetch-marked request", status)
	}
	if got := w.Header().Get(LocationHeader); got != "/login" {
		t.Fatalf("Collage-Location = %q, want /login", got)
	}
}
```

`pkg/collage/guard_test.go` — the integration tests. App scaffold follows `http_test.go`'s `langApp` pattern:

```go
package collage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// guardedApp builds a one-page app whose layout guards every request that does
// not carry the header, mirroring a logged-in reader. renderCount counts data
// handler runs so a test can prove the guard ran before a cached render.
func guardedApp(t *testing.T, strategy func(*collage.PageBuilder), cache bool) (*collage.App, *int) {
	t.Helper()
	renderCount := 0
	cfg := &collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 0},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"templates/layouts/private.html": {Data: []byte(`<div class="auth">{{slot "content"}}</div>`)},
				"templates/pages/panel.html":     {Data: []byte(`<p>panel</p>`)},
			},
			Root: "templates",
		},
	}
	if cache {
		cfg.Cache = collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Minute}
	}
	app, err := collage.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	guard := func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
		if r.Header.Get("X-Logged-In") != "" {
			return nil, nil
		}
		return &collage.GuardDecision{
			Status:   http.StatusSeeOther,
			Location: "/login?next=" + url.QueryEscape(r.URL.RequestURI()),
		}, nil
	}
	layout := collage.NewFragment("private", "layouts/private.html").WithGuard(guard).Build()
	content := collage.NewFragment("panel", "pages/panel.html").
		WithDataHandler(collage.Effect(func(ctx context.Context, rc *collage.RenderContext) error {
			renderCount++
			return nil
		})).
		Build()
	b := collage.NewPage("panel").WithLayouts(layout).WithContent(content).WithPath("en", "/panel")
	if strategy != nil {
		strategy(b)
	}
	if err := app.RegisterPage(b.Build()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	return app, &renderCount
}

func get(h http.Handler, target, header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if header != "" {
		req.Header.Set("X-Logged-In", header)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestGuardRedirectsLoggedOut(t *testing.T) {
	app, _ := guardedApp(t, nil, false)
	w := get(app.Handler(), "/panel", "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/login?next=%2Fpanel" {
		t.Fatalf("Location = %q", got)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("redirect carried a body: %q", w.Body.String())
	}
}

func TestGuardAllowsLoggedIn(t *testing.T) {
	app, _ := guardedApp(t, nil, false)
	w := get(app.Handler(), "/panel", "1")
	if w.Code != http.StatusOK || w.Body.String() != `<div class="auth"><p>panel</p></div>` {
		t.Fatalf("status = %d body = %q", w.Code, w.Body.String())
	}
}

// The cache must not outrank the guard: the first reader's render is cached,
// and a blocked second reader still never sees it.
func TestGuardRunsBeforeCache(t *testing.T) {
	app, renders := guardedApp(t, func(b *collage.PageBuilder) { b.Static() }, true)
	h := app.Handler()
	if w := get(h, "/panel", "1"); w.Code != http.StatusOK {
		t.Fatalf("allowed request: status = %d", w.Code)
	}
	if w := get(h, "/panel", "1"); w.Code != http.StatusOK || *renders != 1 {
		t.Fatalf("second allowed request should hit cache: status = %d renders = %d", w.Code, *renders)
	}
	if w := get(h, "/panel", ""); w.Code != http.StatusSeeOther {
		t.Fatalf("blocked request on cached page: status = %d, want 303", w.Code)
	}
}

func TestGuardOnHeadRequest(t *testing.T) {
	app, _ := guardedApp(t, nil, false)
	req := httptest.NewRequest(http.MethodHead, "/panel", nil)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
}

func TestGuardErrorFailsRequest(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 0},
		Template: collage.TemplateConfig{
			FS:   fstest.MapFS{"templates/layouts/p.html": {Data: []byte(`{{slot "content"}}`)}},
			Root: "templates",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	layout := collage.NewFragment("p", "layouts/p.html").
		WithGuard(func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
			return nil, errBoom
		}).
		Build()
	page := collage.NewPage("x").WithLayouts(layout).
		WithContent(collage.NewFragment("c", "layouts/p.html").Build()).
		WithPath("en", "/x").Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

var errBoom = errBoomType{}

type errBoomType struct{}

func (errBoomType) Error() string { return "boom" }

// A blocked request never reaches the page, so a plugin watching page
// resolution is not told about it.
func TestGuardBlocksBeforePageResolved(t *testing.T) {
	spy := &guardSpy{}
	cfg := &collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 0},
		Template: collage.TemplateConfig{
			FS:   fstest.MapFS{"templates/layouts/p.html": {Data: []byte(`{{slot "content"}}`)}},
			Root: "templates",
		},
		Plugins: []collage.Plugin{spy},
	}
	app, err := collage.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	layout := collage.NewFragment("p", "layouts/p.html").
		WithGuard(func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
			return &collage.GuardDecision{Status: http.StatusUnauthorized}, nil
		}).
		Build()
	page := collage.NewPage("x").WithLayouts(layout).
		WithContent(collage.NewFragment("c", "layouts/p.html").Build()).
		WithPath("en", "/x").Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if spy.resolved != 0 {
		t.Fatalf("PageResolved fired %d times for a blocked request", spy.resolved)
	}
}

type guardSpy struct{ resolved int }

func (s *guardSpy) Name() string                             { return "guard-spy" }
func (s *guardSpy) Version() string                          { return "0.0.0" }
func (s *guardSpy) Shutdown(context.Context) error           { return nil }
func (s *guardSpy) Init(context.Context, collage.Host) error { return nil }
func (s *guardSpy) OnPageResolved(ctx context.Context, ev *collage.PageResolvedEvent) error {
	s.resolved++
	return nil
}

// A page's fallback renders carry no guards: an error page that happens to be
// private still renders for whoever hit the error, rather than redirecting
// the reader into a loop.
func TestGuardSkippedOnFallbackRender(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 0},
		Template: collage.TemplateConfig{
			FS: fstest.MapFS{
				"templates/layouts/private.html": {Data: []byte(`<div class="auth">{{slot "content"}}</div>`)},
				"templates/pages/panel.html":     {Data: []byte(`<p>panel</p>`)},
				"templates/pages/broken.html":    {Data: []byte(`<p>broken</p>`)},
			},
			Root: "templates",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The private error page.
	private := collage.NewFragment("private", "layouts/private.html").
		WithGuard(func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
			return &collage.GuardDecision{Status: http.StatusSeeOther, Location: "/login"}, nil
		}).
		Build()
	errorPage := collage.NewPage("error-page").
		WithLayouts(private).
		WithContent(collage.NewFragment("panel", "pages/panel.html").Build()).
		WithPath("en", "/error-page").
		Build()
	if err := app.RegisterPage(errorPage); err != nil {
		t.Fatalf("RegisterPage(error-page): %v", err)
	}
	// The public page whose render fails.
	broken := collage.NewPage("broken").
		WithContent(collage.NewFragment("broken", "pages/broken.html").
			WithDataHandler(collage.Load(func(ctx context.Context, rc *collage.RenderContext) (string, error) {
				return "", errBoom
			})).
			Required().
			Build()).
		WithErrorPage(errorPage).
		WithPath("en", "/broken").
		Build()
	if err := app.RegisterPage(broken); err != nil {
		t.Fatalf("RegisterPage(broken): %v", err)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/broken", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 with the error page rendered, not a guard redirect", w.Code)
	}
	if !strings.Contains(w.Body.String(), `<div class="auth">`) {
		t.Fatalf("error page did not render: %q", w.Body.String())
	}
}

// Both locales of a guarded page are guarded: the guard sees the request as
// routed, locale prefix included, and the next parameter carries what the
// reader actually asked for.
func TestGuardSeesLocalePrefixedPath(t *testing.T) {
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 0},
		Locale:   collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}, PrefixDefault: true},
		Template: collage.TemplateConfig{
			FS:   fstest.MapFS{"templates/layouts/p.html": {Data: []byte(`{{slot "content"}}`)}},
			Root: "templates",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var seen string
	layout := collage.NewFragment("p", "layouts/p.html").
		WithGuard(func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
			seen = r.URL.RequestURI()
			return &collage.GuardDecision{Status: http.StatusSeeOther, Location: "/tr/login"}, nil
		}).
		Build()
	page := collage.NewPage("x").WithLayouts(layout).
		WithContent(collage.NewFragment("c", "layouts/p.html").Build()).
		WithPath("en", "/x").WithPath("tr", "/x").Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tr/x", nil))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/tr/login" {
		t.Fatalf("status = %d Location = %q", w.Code, w.Header().Get("Location"))
	}
	if seen != "/tr/x" {
		t.Fatalf("guard saw %q, want /tr/x", seen)
	}
}
```

Config field names verified against `pkg/collage/config.go`: `Config.Locale LocaleConfig` with `Default`, `Supported`, `PrefixDefault`, and `Config.Plugins []Plugin` (the field the user's own application snippet uses).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpx/ -run TestRunGuards -v && go test ./pkg/collage/ -run TestGuard -v`
Expected: FAIL — `runGuards` undefined; integration tests get 200 (no dispatch yet).

- [ ] **Step 3: Write the implementation**

`internal/httpx/guard.go`:

```go
package httpx

import (
	"context"
	"net/http"

	"github.com/Elagoht/collage/internal/types"
)

// runGuards asks each guard, in order, whether the request may pass, and stops
// at the first that answers: a decision is the answer, an error is a failure
// that means no later guard runs. nil throughout allows the request.
func runGuards(ctx context.Context, r *http.Request, guards []types.GuardFunc) (*types.GuardDecision, error) {
	for _, guard := range guards {
		decision, err := guard(ctx, r)
		if err != nil {
			return nil, err
		}
		if decision != nil {
			return decision, nil
		}
	}
	return nil, nil
}

// checkGuards runs guards and writes what they answer. It is the guard half of
// every guarded dispatch — a page render, an action on a page's own URL — so
// the two cannot drift apart. A nil guards slice allows immediately.
func (h *Handler) checkGuards(w http.ResponseWriter, r *http.Request, route *routeRef, locale string, guards []types.GuardFunc) (int, bool) {
	if len(guards) == 0 {
		return 0, true
	}
	decision, err := runGuards(r.Context(), r, guards)
	if err != nil {
		f := route.failure(http.StatusInternalServerError, stageGuard, err)
		f.locale = locale
		return h.serveFailure(w, r, f), false
	}
	if decision == nil {
		return 0, true
	}
	if err := decision.Validate(); err != nil {
		f := route.failure(http.StatusInternalServerError, stageGuard, err)
		f.locale = locale
		return h.serveFailure(w, r, f), false
	}
	if decision.Location != "" {
		status := decision.Status
		if status == 0 {
			status = http.StatusSeeOther
		}
		return writeRedirect(w, r, status, decision.Location), false
	}
	w.WriteHeader(decision.Status)
	return decision.Status, false
}
```

In `internal/httpx/handler.go`, add to the stage constants (~line 89):

```go
	stageGuard = "guard"
```

Extract `writeRedirect` in `internal/httpx/action.go` from `writeActionResult`'s Location branch, and call it from there (behaviour identical):

```go
// writeRedirect answers with a redirect, honouring the fetch convention: a
// script marked the request with FetchHeader would rather be handed the
// destination than redirected to it, because fetch follows redirects itself
// and the script would navigate to the page and have it rendered twice.
func writeRedirect(w http.ResponseWriter, r *http.Request, status int, location string) int {
	if r.Header.Get(FetchHeader) != "" {
		w.Header().Set(LocationHeader, location)
		w.WriteHeader(http.StatusNoContent)
		return http.StatusNoContent
	}
	w.Header().Set("Location", location)
	w.WriteHeader(status)
	return status
}
```

(`writeActionResult`'s branch becomes `return writeRedirect(w, r, status, result.Location)` with its comment kept.)

Insert in `serve`, between `route.at(pagePattern, match.Locale)` and the `PageResolved` dispatch:

```go
	// Guards before the cache and before PageResolved: a blocked reader
	// reaches neither the page's cached render nor the plugins that watch a
	// page being reached — the request never touched the page.
	if status, allowed := h.checkGuards(w, r, route, match.Locale, page.Guards()); !allowed {
		return status
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/httpx/ ./pkg/collage/`
Expected: PASS. If `TestGuardRunsBeforeCache` fails with a 200 on the blocked request, the dispatch landed after the cache lookup — move it up.

- [ ] **Step 5: Commit**

```bash
git add internal/httpx/guard.go internal/httpx/guard_test.go internal/httpx/handler.go internal/httpx/action.go pkg/collage/guard_test.go
git commit -m "feat: guards dispatch for page renders, before cache and PageResolved"
```

---

### Task 6: Guards on actions and fragment paths

**Files:**
- Modify: `internal/router/action.go:144-157` (`resolve` returns the owning page with an action)
- Modify: `internal/router/router.go` (`MatchResult` doc comment, "Exactly one of Page, Document and Action")
- Modify: `internal/httpx/action.go:52` (`serveAction` runs `match.Page`'s guards first)
- Modify: `internal/core/action.go:134-164` (`registerFragmentPaths` wraps the handler with the fragment's own guard)
- Test: `internal/router/action_test.go`, `pkg/collage/guard_test.go`

**Interfaces:**
- Consumes: `checkGuards` (Task 5), `GuardDecision.Validate` (Task 1).
- Produces: the `MatchResult` contract change — `Page` is non-nil alongside `Action` when the action shares a page's URL; fragment-path actions run their fragment's `Guard` inside their handler and answer with `ActionResult{Status, Location}`.

- [ ] **Step 0: Verify RenderPath's synthetic request is a GET**

Run: `grep -n "syntheticRequest" internal/core/app.go` and read the function. It must issue `http.MethodGet` (a GET resolves to the page, never the action, so `RenderPath` behaviour is unchanged by the `resolve` contract change). If it does not, stop and reassess: paths that resolved to "no page" (action-only) would start rendering.

- [ ] **Step 1: Write the failing tests**

In `internal/router/action_test.go`, add (`router.New(LocaleOptions{...}) Router`, `Register(page)`, `RegisterAction(action)`, `Match(req)` are the package's own names, verified):

```go
// An action on a page's own URL must carry the page: the page is what a guard
// applies to, and the handler has no other way to reach it.
func TestResolveReturnsOwningPageWithAction(t *testing.T) {
	page := &types.Page{Name: "p", Paths: map[string]string{"en": "/p"}}
	rt := New(LocaleOptions{Default: "en"})
	if err := rt.Register(page); err != nil {
		t.Fatalf("Register: %v", err)
	}
	action := &types.Action{Name: "save", Paths: map[string]string{"en": "/p"}, Methods: []string{http.MethodPost}, Handler: func(ctx context.Context, rc *types.RenderContext) (*types.ActionResult, error) { return nil, nil }}
	if err := rt.RegisterAction(action); err != nil {
		t.Fatalf("RegisterAction: %v", err)
	}
	match, err := rt.Match(httptest.NewRequest(http.MethodPost, "/p", nil))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if match.Action == nil || match.Page != page {
		t.Fatalf("match = action %v page %v, want the action and its owning page", match.Action, match.Page)
	}
	// A standalone action still matches alone: no page, no spine.
	solo := &types.Action{Name: "hook", Paths: map[string]string{"en": "/hook"}, Methods: []string{http.MethodPost}, Handler: func(ctx context.Context, rc *types.RenderContext) (*types.ActionResult, error) { return nil, nil }}
	if err := rt.RegisterAction(solo); err != nil {
		t.Fatalf("RegisterAction: %v", err)
	}
	match, err = rt.Match(httptest.NewRequest(http.MethodPost, "/hook", nil))
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if match.Action != solo || match.Page != nil {
		t.Fatalf("standalone match = page %v, want nil", match.Page)
	}
}
```

Append to `pkg/collage/guard_test.go`:

```go
func post(h http.Handler, target, header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, nil)
	if header != "" {
		req.Header.Set("X-Logged-In", header)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// A form posts to the page it sits on; a page a reader may not see is a page
// whose form the reader may not submit.
func TestGuardCoversPageAttachedAction(t *testing.T) {
	app, _ := guardedApp(t, nil, false)
	page := collage.NewPage("save").
		WithLayouts(collage.NewFragment("private", "layouts/private.html").
			WithGuard(func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
				if r.Header.Get("X-Logged-In") != "" {
					return nil, nil
				}
				return &collage.GuardDecision{Status: http.StatusSeeOther, Location: "/login"}, nil
			}).Build()).
		WithContent(collage.NewFragment("c", "layouts/private.html").Build()).
		WithPath("en", "/save").
		WithAction(http.MethodPost, func(ctx context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			return &collage.ActionResult{Status: http.StatusOK}, nil
		}).
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if w := post(app.Handler(), "/save", ""); w.Code != http.StatusSeeOther {
		t.Fatalf("logged-out POST: status = %d, want 303", w.Code)
	}
	if w := post(app.Handler(), "/save", "1"); w.Code != http.StatusOK {
		t.Fatalf("logged-in POST: status = %d, want 200", w.Code)
	}
}

// A standalone action has no page, so no spine: guarding it is not something
// the framework invents for it.
func TestGuardSkipsStandaloneAction(t *testing.T) {
	app, _ := guardedApp(t, nil, false)
	action := collage.NewAction("webhook").
		WithPath("en", "/hook").
		WithMethods(http.MethodPost).
		WithHandler(func(ctx context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			return &collage.ActionResult{Status: http.StatusTeapot}, nil
		}).
		Build()
	if err := app.RegisterAction(action); err != nil {
		t.Fatalf("RegisterAction: %v", err)
	}
	if w := post(app.Handler(), "/hook", ""); w.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418 (no guard to run)", w.Code)
	}
}

// A fragment path is its own route: its fragment's guard is its whole policy,
// and nothing flows down from the page that declared it.
func TestFragmentPathOwnGuard(t *testing.T) {
	app, _ := guardedApp(t, nil, false)
	frag := collage.NewFragment("results", "layouts/private.html").
		WithGuard(func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
			if r.Header.Get("X-Logged-In") != "" {
				return nil, nil
			}
			return &collage.GuardDecision{Status: http.StatusUnauthorized}, nil
		}).
		Build()
	page := collage.NewPage("search").
		WithLayouts(collage.NewFragment("private", "layouts/private.html").
			WithGuard(func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
				t.Fatal("the declaring page's guard ran for a fragment path")
				return nil, nil
			}).Build()).
		WithContent(collage.NewFragment("c", "layouts/private.html").Build()).
		WithPath("en", "/search").
		WithFragmentPath("en", "/search/results", frag).
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if w := get(app.Handler(), "/search/results", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if w := get(app.Handler(), "/search/results", "1"); w.Code != http.StatusOK {
		t.Fatalf("allowed: status = %d, want 200", w.Code)
	}
}

// The leak pin: a redirect decision with no Location must fail the request,
// never fall through to rendering the fragment.
func TestFragmentPathMalformedDecisionFails(t *testing.T) {
	app, _ := guardedApp(t, nil, false)
	frag := collage.NewFragment("results", "layouts/private.html").
		WithGuard(func(ctx context.Context, r *http.Request) (*collage.GuardDecision, error) {
			return &collage.GuardDecision{Status: http.StatusSeeOther}, nil // no Location
		}).
		Build()
	page := collage.NewPage("search").
		WithContent(collage.NewFragment("c", "layouts/private.html").Build()).
		WithPath("en", "/search").
		WithFragmentPath("en", "/search/results", frag).
		Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if w := get(app.Handler(), "/search/results", "1"); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, not the fragment's render", w.Code)
	}
}
```

Names verified against the tree: `ActionBuilder` has `WithPath`, `WithMethods` (plural), `WithHandler`, `WithoutCSRF`, `Build`; `App` is an alias of `core.App`, so `app.RegisterAction(action *Action)` is public. CSRF only verifies when `Config.CSRFKey` is set, and no test above sets it, so the bare POSTs pass through unverified — no token plumbing needed.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/router/ -run TestResolveReturnsOwningPageWithAction -v && go test ./pkg/collage/ -run 'TestGuardCovers|TestGuardSkips|TestFragmentPath' -v`
Expected: FAIL — `resolve` returns a nil page; actions and fragment paths answer unguarded.

- [ ] **Step 3: Write the implementation**

`internal/router/action.go` — `resolve`, both action branches:

```go
	if a, ok := n.actions[method]; ok {
		// The page comes with the action when the action shares its URL: the
		// handler never needs it, but a guard does — the page's spine is what
		// a page-attached action inherits its protection from.
		return n.page, nil, a, true
	}
	if method == http.MethodHead {
		if a, ok := n.actions[http.MethodGet]; ok {
			return n.page, nil, a, true
		}
	}
```

`internal/router/router.go` — update the `MatchResult` field doc for `Page` and the "exactly one" sentence (find both comments): Page is set alone for a page render, and alongside `Action` when the action is one of a page's own — never alongside `Document`.

`internal/httpx/action.go` — at the top of `serveAction`, after the nil-handler check:

```go
	// The page's guards, not the action's own: an action has no spine to
	// guard, but one on a page's URL is reached through that page, and a page
	// a reader may not see is a page whose form the reader may not submit.
	// Before the body limit and the CSRF check — a guard that answers never
	// reads the body, and a logged-out forged POST is better spent against the
	// guard than the forgery check.
	if page := match.Page; page != nil {
		if status, allowed := h.checkGuards(w, r, route, match.Locale, page.Guards()); !allowed {
			return status
		}
	}
```

`internal/core/action.go` — wrap the fragment-path handler:

```go
			action := &types.Action{
				Name:    page.Name + ":" + fragment.Name,
				Paths:   map[string]string{locale: pattern},
				Methods: []string{"GET"},
				Handler: func(ctx context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
					// A fragment path is its own route; its fragment's guard
					// is its whole policy, and nothing flows down from the
					// page that declared it. The decision is validated before
					// it becomes a result: a redirect with no Location would
					// fall through the result's own switch — Location, then
					// Fragment — and render the fragment the guard refused.
					if target.Guard != nil {
						decision, err := target.Guard(ctx, rc.Request)
						if err != nil {
							return nil, fmt.Errorf("collage: fragment %q: %w", target.Name, err)
						}
						if decision != nil {
							if err := decision.Validate(); err != nil {
								return nil, fmt.Errorf("collage: fragment %q: %w", target.Name, err)
							}
							return &types.ActionResult{Status: decision.Status, Location: decision.Location}, nil
						}
					}
					return &types.ActionResult{Fragment: target}, nil
				},
			}
```

(`writeActionResult` already gives a zero-status Location its 303 and honors the fetch convention, so a guarded fragment path answers a fetch() with 204 + `Collage-Location` for free.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS. Grep for tests that asserted `match.Page == nil` for action matches (`grep -rn "Page == nil\|Page, want nil" internal/router/*_test.go internal/httpx/*_test.go`) and update them to the new contract.

- [ ] **Step 5: Commit**

```bash
git add internal/router/action.go internal/router/router.go internal/router/action_test.go internal/httpx/action.go internal/core/action.go pkg/collage/guard_test.go
git commit -m "feat: guards cover page actions and a fragment path's own guard"
```

---

### Task 7: Inspect reports chains and guards

**Files:**
- Modify: `internal/core/inspect.go:39-50` (`InspectedPage`), `:178-195` (its construction)
- Test: `internal/core/inspect_test.go`, `pkg/collage/inspect_test.go`

**Interfaces:**
- Consumes: `Page.LayoutChain`, `Page.Guards` order (Task 2).
- Produces: `InspectedPage.Layouts []string` (`json:"layouts,omitempty"`, outermost first) and `InspectedPage.Guards []string` (`json:"guards,omitempty"`, spine order); `Layout` removed. `collage inspect` JSON changes shape — the VS Code extension's catalog is regenerated outside this repo after release.

- [ ] **Step 1: Write the failing test**

In `internal/core/inspect_test.go`, find the page inspection test and extend it (or add):

```go
func TestInspectedPageLayoutsAndGuards(t *testing.T) {
	// Build and register an app whose page has a two-layout chain, the outer
	// carrying a guard, through the file's existing helper; then:
	insp := app.Inspect() // use the real accessor the file's tests already use
	var page *InspectedPage
	for i := range insp.Pages {
		if insp.Pages[i].Name == "panel" {
			page = &insp.Pages[i]
		}
	}
	if page == nil {
		t.Fatal("page not inspected")
	}
	if len(page.Layouts) != 2 || page.Layouts[0] != "private" || page.Layouts[1] != "auth" {
		t.Fatalf("Layouts = %v, want [private auth]", page.Layouts)
	}
	if len(page.Guards) != 1 || page.Guards[0] != "private" {
		t.Fatalf("Guards = %v, want [private]", page.Guards)
	}
}
```

Align with the file's real construction helpers and accessor names; the assertion is the deliverable. Update any existing assertion on `InspectedPage.Layout` to `Layouts`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run TestInspectedPage -v`
Expected: FAIL — `Layouts`/`Guards` undefined.

- [ ] **Step 3: Write the implementation**

In `InspectedPage`, replace the `Layout` field with:

```go
	// Layouts are the page's layout chain, outermost first, by fragment name.
	Layouts []string `json:"layouts,omitempty"`
	// Guards are the spine fragments carrying a guard, in the order they are
	// asked: the chain outermost first, then the content fragment.
	Guards []string `json:"guards,omitempty"`
```

In its construction (inspect.go ~180), replace the `ip.Layout` assignment:

```go
		for _, f := range p.LayoutChain {
			if f == nil {
				continue
			}
			ip.Layouts = append(ip.Layouts, f.Name)
			if f.Guard != nil {
				ip.Guards = append(ip.Guards, f.Name)
			}
		}
		if p.ContentFragment != nil && p.ContentFragment.Guard != nil {
			ip.Guards = append(ip.Guards, p.ContentFragment.Name)
		}
```

Also update the fragments walk at inspect.go:194 (`roots := append(... p.LayoutFragment, ...)`) to walk `p.LayoutChain` instead — the folded copies render identically but report copy names to `seen` maps; the originals are what the author wrote. Read the surrounding function first and keep its behaviour for pages with no chain (append nothing).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ ./pkg/collage/`
Expected: PASS, including `pkg/collage/inspect_test.go` (update its JSON-shape fixtures from `layout` to `layouts`/`guards`).

- [ ] **Step 5: Commit**

```bash
git add internal/core/inspect.go internal/core/inspect_test.go pkg/collage/inspect_test.go
git commit -m "feat: inspect reports a page's layout chain and guards"
```

---

### Task 8: Docs and changelog

**Files:**
- Modify: `docs/fragments.md` (the "Pages, layouts, and the content slot" section — chains; Task 4 already moved code samples to `WithLayouts`)
- Modify: `docs/routing.md` (new "Guards" section)
- Modify: `docs/plugins.md` (PageResolved wording: blocked requests never fire it; stage list gains `guard` if the file lists stages — grep first)
- Modify: `CHANGELOG.md` (new `## Unreleased` section at the top)

**Interfaces:**
- Consumes: everything shipped above.
- Produces: user-facing documentation. (The docs site, EN+TR, and the `collage-session` `RequireUser` release are follow-ups in their own repos after the core release.)

- [ ] **Step 1: fragments.md — chains**

In the "Pages, layouts, and the content slot" section, after the single-layout prose, add:

```markdown
### Layout chains

A page wraps its content in as many layouts as it has, outermost first.
Registration fills every `content` slot in the chain — the content fragment
into the innermost layout, each layout into the one outside it — so a layout
with a hole is a finished fragment, not a builder a page has to finish:

```go
page := collage.NewPage("login").
    WithLayouts(layouts.Master(), layouts.Auth()).
    WithContent(content).
    WithPath("en", "/login").
    Build()
```

Every page still gets its own copy of every layout's slot table, so one layout
value serves the whole application. A layout in a chain must not arrive with
its `content` slot filled — except the innermost holding exactly the page's
content fragment, the hand-bound escape hatch a single layout has always had.
```

- [ ] **Step 2: routing.md — guards**

Add a section (after the redirects material, or wherever access control sits most naturally in the file's flow):

```markdown
## Guards

A guard decides whether a request may reach a page, and it is declared where
the page's structure is — on a layout, or on the content fragment:

```go
private := collage.NewFragment("private", "layouts/private.html").
    WithGuard(session.RequireUser("/login")).
    Build()

page := collage.NewPage("dashboard").
    WithLayouts(layouts.Master(), private).
    WithContent(dashboard).
    WithPath("en", "/dashboard").
    Build()
```

Every page whose spine carries the layout — its renders and the actions on
its own URL alike — asks the guard first. The guard runs after the router
resolves the request and before the page's cache is read: a blocked reader
never reaches a cached render, so a private page may be `Static()`. Allowed
readers share the page's cache; a page that differs per reader is a
personalisation question, not a guard question.

A guard answers by allowing (a nil decision), redirecting (a 3xx status with
a location — zero means 303), or refusing (401, 403, …). A guard that returns
an error fails the request. Guards run outermost first, and the first one
that answers decides.

A fragment path is its own route: the fragment's own guard is its whole
policy, and it inherits nothing from the page that declared it. Error and
not-found pages render without guards — a private error page would redirect
to itself. What a guard *checks* is not the framework's: the session plugin's
`session.RequireUser` is one policy, and any function with the shape is
another.
```

- [ ] **Step 3: plugins.md and CHANGELOG**

In `plugins.md`, find the `PageResolvedHook` description and append: "A request a guard blocked never fires it — the request never reached the page." Grep `stage` in the file; if the pipeline stages are listed, add `guard`.

`CHANGELOG.md`, new section directly under `# Changelog`:

```markdown
## Unreleased

### Added

- **`WithLayouts`**: a page's layout chain, outermost first. Registration
  folds the chain — the content fragment into the innermost layout, each
  layout into the next outer one — so a layout with a hole is a finished
  fragment, and nesting layouts is a list, not nested builders. `WithLayout`
  is removed; pre-1.0, no compatibility is kept.
- **`WithGuard`** on fragments: a guard runs for every page whose spine the
  fragment is on — after routing, before the page's cache is read, before
  `PageResolved` — and covers the actions on the page's own URL. A fragment
  path runs its fragment's own guard and inherits nothing. `collage inspect`
  reports `layouts` and `guards` per page (`layout` is gone).

### Changed

- Registration now refuses a layout chain whose outer layouts arrive with a
  filled `content` slot, where it previously bound alongside the stray fill
  and rendered both.
```

- [ ] **Step 4: Verify docs examples compile against reality**

Run: `grep -rn "WithLayout(" docs/ README.md; grep -rn "session.RequireUser" docs/`
Expected: first grep empty (Task 4 caught the samples; this re-checks). The second is intentional — it documents the session plugin's API due in its next release; leave it, the changelog notes the pairing.

- [ ] **Step 5: Full suite and commit**

Run: `go test ./... && go vet ./...`

```bash
git add docs/fragments.md docs/routing.md docs/plugins.md CHANGELOG.md
git commit -m "docs: layout chains, guards, and the inspect shape change"
```

---

## Out of this repo (follow-ups, not tasks)

- `github.com/Elagoht/collage-session`: add `RequireUser(loginPath string) collage.GuardFunc` (303 + `?next=` for the logged-out; allow for the logged-in), release after the core version that ships `GuardFunc`.
- Docs site (`~/Desktop/collage-docs`, EN+TR): layout chains and guards pages, after the core release.
- VS Code extension: regenerate the inspect catalog against the new `layouts`/`guards` shape.

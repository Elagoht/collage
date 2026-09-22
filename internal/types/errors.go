package types

import "errors"

// ErrNilFragment is returned when a nil *Fragment is used where a non-nil fragment is
// required — as a Bind child, a Validate receiver, or elsewhere a fragment is expected.
var ErrNilFragment = errors.New("collage: nil fragment")

// ErrUnknownSlot is returned when an operation references a slot name a fragment has
// not declared, or when a fragment's slot map contains a malformed entry (an empty
// slot name, or a map key that does not match the SlotDefinition's own Name field).
var ErrUnknownSlot = errors.New("collage: unknown slot")

// ErrSlotOccupied is returned when binding a fragment to a slot that already has a
// fill and does not allow multiple.
var ErrSlotOccupied = errors.New("collage: slot already occupied")

// ErrFragmentCycle is returned when a fragment tree contains a fragment reachable
// from itself.
var ErrFragmentCycle = errors.New("collage: fragment cycle detected")

// ErrMissingContent is returned when a page has no content fragment, or when a
// fragment's declared Required slot has no fill.
var ErrMissingContent = errors.New("collage: missing content")

// ErrMissingTTL is returned when a page uses StrategyIncremental without a positive
// CacheTTL.
var ErrMissingTTL = errors.New("collage: missing cache ttl for incremental strategy")

// ErrInvalidRedirectStatus is returned when a redirect's status code is set to a
// value other than 0, 301, 302, 307, or 308.
var ErrInvalidRedirectStatus = errors.New("collage: invalid redirect status code")

// ErrSelfErrorPage is returned when a page references itself as its own NotFoundPage
// or ErrorPage.
var ErrSelfErrorPage = errors.New("collage: page cannot reference itself as an error page")

// ErrEmptyName is returned when a required Name field is empty.
var ErrEmptyName = errors.New("collage: empty name")

// ErrEmptyTemplatePath is returned when a fragment's TemplatePath is empty.
var ErrEmptyTemplatePath = errors.New("collage: empty template path")

// ErrInvalidPath is returned when a path pattern does not start with "/".
var ErrInvalidPath = errors.New("collage: invalid path")

package types

import (
	"maps"
	"slices"
)

// The Clone functions copy a built value so the builder that made it can go on
// being written without reaching it: the struct is copied, unexported fields
// included, and every slice and map field is cloned. Pointers to other built
// values — a layout, a content fragment, an error page, an attached action — stay
// shared: they are values of their own, which their builders already copied.
//
// A container field added to one of these types later must be cloned here;
// TestClone_NoSharedContainers fails until it is.

// ClonePage returns a copy of p that shares no slice or map with it.
func ClonePage(p *Page) *Page {
	if p == nil {
		return nil
	}
	c := *p
	c.LayoutChain = slices.Clone(p.LayoutChain)
	c.Paths = maps.Clone(p.Paths)
	c.Redirects = slices.Clone(p.Redirects)
	c.CacheParams = slices.Clone(p.CacheParams)
	c.DependencyTags = slices.Clone(p.DependencyTags)
	c.Actions = slices.Clone(p.Actions)
	if p.FragmentPaths != nil {
		c.FragmentPaths = make(map[string]map[string]*Fragment, len(p.FragmentPaths))
		for locale, byPattern := range p.FragmentPaths {
			c.FragmentPaths[locale] = maps.Clone(byPattern)
		}
	}
	return &c
}

// CloneFragment returns a copy of f that shares no slice or map with it. Each
// slot definition is copied too, its Fill cloned, since a builder binds into and
// constrains a slot in place.
func CloneFragment(f *Fragment) *Fragment {
	if f == nil {
		return nil
	}
	c := *f
	if f.Slots != nil {
		c.Slots = make(map[string]*SlotDefinition, len(f.Slots))
		for name, slot := range f.Slots {
			if slot == nil {
				c.Slots[name] = nil
				continue
			}
			d := *slot
			d.Fill = slices.Clone(slot.Fill)
			c.Slots[name] = &d
		}
	}
	return &c
}

// CloneDocument returns a copy of d that shares no slice or map with it.
func CloneDocument(d *Document) *Document {
	if d == nil {
		return nil
	}
	c := *d
	c.Paths = maps.Clone(d.Paths)
	c.Body = slices.Clone(d.Body)
	c.CacheParams = slices.Clone(d.CacheParams)
	c.DependencyTags = slices.Clone(d.DependencyTags)
	c.Redirects = slices.Clone(d.Redirects)
	return &c
}

// CloneAction returns a copy of a that shares no slice or map with it.
func CloneAction(a *Action) *Action {
	if a == nil {
		return nil
	}
	c := *a
	c.Paths = maps.Clone(a.Paths)
	c.Methods = slices.Clone(a.Methods)
	return &c
}

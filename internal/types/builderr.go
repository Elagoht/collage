package types

import "errors"

// RecordBuildErr keeps on a built fragment, page or document the mistakes its
// builder recorded, so registration can refuse it even when nobody called
// BuildErr. The builders in pkg/collage call it from Build.
func RecordBuildErr(target interface{ setBuildErr(error) }, err error) {
	target.setBuildErr(err)
}

func (f *Fragment) setBuildErr(err error) { f.buildErr = err }
func (p *Page) setBuildErr(err error)     { p.buildErr = err }
func (d *Document) setBuildErr(err error) { d.buildErr = err }

// FragmentBuildErr returns what f's builder recorded, for f alone.
func FragmentBuildErr(f *Fragment) error {
	if f == nil {
		return nil
	}
	return f.buildErr
}

// DocumentBuildErr returns what d's builder recorded.
func DocumentBuildErr(d *Document) error {
	if d == nil {
		return nil
	}
	return d.buildErr
}

// PageBuildErr returns what p's builder recorded, together with what the builders
// of every fragment reachable from it recorded — through its layout and content,
// their slots and their fallbacks.
func PageBuildErr(p *Page) error {
	if p == nil {
		return nil
	}
	errs := []error{p.buildErr}
	seen := make(map[*Fragment]bool)
	var walk func(*Fragment)
	walk = func(f *Fragment) {
		if f == nil || seen[f] {
			return
		}
		seen[f] = true
		errs = append(errs, f.buildErr)
		for _, slot := range f.Slots {
			if slot == nil {
				continue
			}
			for _, child := range slot.Fill {
				walk(child)
			}
		}
		walk(f.Fallback)
	}
	walk(p.LayoutFragment)
	walk(p.ContentFragment)
	return errors.Join(errs...)
}

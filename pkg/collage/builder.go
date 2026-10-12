package collage

import "fmt"

// Helpers the page, document and action builders share.

// checkPathLocale reports why WithPath must not write locale into paths: it is
// empty, or it already holds a path. kind and name say whose paths they are.
func checkPathLocale(paths map[string]string, locale, kind, name string) error {
	if locale == "" {
		return fmt.Errorf("%w: WithPath on %s %q", ErrInvalidLocale, kind, name)
	}
	if _, taken := paths[locale]; taken {
		return fmt.Errorf("%w: %q on %s %q", ErrDuplicateLocalePath, locale, kind, name)
	}
	return nil
}

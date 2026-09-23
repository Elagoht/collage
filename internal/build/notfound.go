package build

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// writeNotFoundPages writes the site's 404.html, one per locale.
//
// A static host answers an unknown URL with the site's own 404.html, and an export
// without one answers with whatever that host decided to show — which is a page
// from somebody else's site, in somebody else's language, with none of the
// navigation a reader needs to get back.
//
// One per locale, at the root of each locale's output, because that is where a host
// looks: "/404.html" for the default locale's tree and "/tr/404.html" for a Turkish
// one. Hosts differ on whether they consult the nested one; writing both costs a
// page and covers either.
//
// An application that registered no not-found page gets no file and no complaint.
// It declared nothing, so there is nothing to report.
func (b *Builder) writeNotFoundPages(ctx context.Context, outDirResolved string) ([]string, []error) {
	var written []string
	var errs []error

	for _, locale := range b.exportedLocales() {
		result, err := b.app.RenderNotFound(ctx, locale)
		if err != nil {
			errs = append(errs, fmt.Errorf("collage: render not-found page for locale %q: %w", locale, err))
			continue
		}
		if result == nil {
			// No not-found page registered. Nothing to write for any locale.
			return written, errs
		}
		if len(result.HTML) == 0 {
			errs = append(errs, fmt.Errorf("%w: not-found page locale %q", ErrEmptyRender, locale))
			continue
		}
		if marker := b.app.CSRFMarker(); marker != "" && bytes.Contains(result.HTML, []byte(marker)) {
			errs = append(errs, fmt.Errorf("%w: not-found page locale %q", ErrUnresolvedToken, locale))
			continue
		}

		urlPath := localeOutputPath(locale, b.app.DefaultLocale(), "/404.html")
		target, err := documentTarget(outDirResolved, urlPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("collage: not-found page locale %q: %w", locale, err))
			continue
		}
		if err := verifyNoSymlinksBeneath(outDirResolved, target); err != nil {
			errs = append(errs, fmt.Errorf("collage: not-found page locale %q: %w", locale, err))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			errs = append(errs, fmt.Errorf("collage: create directory for %q: %w", target, err))
			continue
		}
		if err := os.WriteFile(target, result.HTML, 0o644); err != nil {
			errs = append(errs, fmt.Errorf("collage: write %q: %w", target, err))
			continue
		}
		written = append(written, target)
	}

	return written, errs
}

// exportedLocales returns every locale this site has pages for, sorted, with the
// default locale included even when nothing declares a path for it.
func (b *Builder) exportedLocales() []string {
	seen := map[string]struct{}{b.app.DefaultLocale(): {}}
	for _, page := range b.app.Pages() {
		for locale := range page.Paths {
			seen[locale] = struct{}{}
		}
	}

	locales := make([]string, 0, len(seen))
	for locale := range seen {
		if locale != "" {
			locales = append(locales, locale)
		}
	}
	sort.Strings(locales)
	return slices.Clip(locales)
}

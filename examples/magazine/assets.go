package main

import (
	"embed"
	"fmt"
	"io/fs"

	"github.com/Elagoht/collage/pkg/collage"
)

// staticFS holds the stylesheet. Embedded, like the templates, so the binary is the
// only artefact a deployment has to move.
//
//go:embed static
var staticFS embed.FS

// assetPrefix is the URL prefix the stylesheet is served under. A mount prefix must
// begin and end with "/" and must not be "/" alone, which would swallow every page
// and document route on the site.
const assetPrefix = "/static/"

// mountStatic serves staticFS under assetPrefix.
//
// fs.Sub strips the embedded "static/" directory, because embed.FS names every file
// by its path in the source tree: without it the stylesheet would answer at
// "/static/static/magazine.css".
//
// The Cache-Control is a declaration, not a default the framework derives: a mounted
// file never enters the page cache, so how long a client may keep it is the mount's
// business alone. Five minutes suits a file that keeps its name across deploys. A
// real deployment fingerprints the filename and says a year with "immutable".
func mountStatic(app *collage.App) error {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return fmt.Errorf("sub static: %w", err)
	}
	return app.Mount(assetPrefix, sub, collage.WithCacheControl("public, max-age=300"))
}

package main

import (
	"embed"
	"fmt"
	"io/fs"

	"github.com/Elagoht/collage/pkg/collage"
)

// assetsFS holds the example's static files. They are embedded rather than read
// from disk so "go run ./examples/blog" serves the same stylesheet from any
// working directory — an os.DirFS or an os.OpenRoot over "static" would resolve
// relative to wherever the process happened to start.
//
// A real application usually wants the opposite trade: files on disk, so a CSS
// change does not need a rebuild. That is os.OpenRoot("./static") and root.FS(),
// never os.DirFS — see docs/assets.md for why the distinction matters.
//
//go:embed static
var assetsFS embed.FS

// assetPrefix is the URL prefix the stylesheet and everything beside it is
// served under. A mount prefix must begin and end with "/", and must not be "/"
// alone: a mount at the root would swallow every page and document route.
const assetPrefix = "/static/"

// stylesheetPath is the URL the shared layout links, and the one file the
// end-to-end test fetches through the mount.
const stylesheetPath = assetPrefix + "app.css"

// mountAssets serves assetsFS under assetPrefix.
//
// fs.Sub strips the embedded "static/" directory back off, because embed.FS
// names every file by its path in the source tree: without it the stylesheet
// would answer at "/static/static/app.css". The mount serves what it is given,
// and does not guess at a root inside it.
//
// The Cache-Control is the one part of a mount's response the framework does not
// derive: a mounted file never enters the page cache, so how long a client may
// keep it is the mount's declaration and nothing else's. An hour is the default;
// it is spelled out here because a real deployment picks this number
// deliberately — a year and "immutable" for fingerprinted filenames, minutes for
// files that keep their names across deploys.
func mountAssets(app *collage.App) error {
	static, err := fs.Sub(assetsFS, "static")
	if err != nil {
		return fmt.Errorf("sub static: %w", err)
	}
	return app.Mount(assetPrefix, static, collage.WithCacheControl("public, max-age=3600"))
}

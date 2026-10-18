package collage

import "github.com/Elagoht/collage/internal/core"

// HandleOption configures a handler mounted with App.Handle. See WithMiddleware.
type HandleOption = core.HandleOption

// WithMiddleware runs a handler mounted with App.Handle inside the middleware
// chain — the application's Use middleware and every plugin's, in the order a
// page gets them — which is what every mount did before v0.67.0. Without it the
// handler runs with no middleware at all.
//
// Use it for a handler that relies on the site's middleware: a basicauth or a
// rate limit registered to guard it, the session it reads, a middleware that
// rewrites requests into its prefix.
//
//	app.Handle("/api/", api, collage.WithMiddleware())
func WithMiddleware() HandleOption { return core.WithMiddleware() }

//go:build !collage_dev

package main

import "embed"

// Templates and static files are embedded, so this binary runs from anywhere:
// a container with a different WORKDIR, a systemd unit, a copy on a server.
//
// Not in development: "collage dev" builds with -tags collage_dev, which leaves
// this file out and embed_dev.go in. Development mode reads both directories
// from disk anyway, and every build that embeds them stores another copy of
// them in the Go build cache — one per save, kept for days.
//
//go:embed all:templates
var templatesFS embed.FS

//go:embed all:static
var staticFS embed.FS

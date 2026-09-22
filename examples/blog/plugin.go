package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"github.com/Elagoht/collage/pkg/collage"
)

// StampMarker is the comment the stamp plugin injects into every freshly rendered
// page. The end-to-end test asserts on it, which is how the plugin path is proven
// to run rather than merely to compile.
const StampMarker = "<!-- rendered by blog-stamp -->"

// bodyClose is the tag the marker is injected in front of.
var bodyClose = []byte("</body>")

// stamp is the example's plugin: it post-processes every rendered page through
// OnAfterRender, and contributes one CLI subcommand through the Host it is given
// at startup.
//
// It is the HTML equivalent of an X-Powered-By header. A hook cannot set a
// response header — OnAfterRender is handed the rendered bytes, not the
// ResponseWriter — so the marker goes into the markup instead, which is also what
// a real analytics-snippet or asset-fingerprinting plugin does.
type stamp struct {
	// store is what the plugin's CLI subcommand reports on.
	store *PostStore
	// logger is the application's own logger, taken from the Host in Init.
	logger *slog.Logger
}

// The plugin implements the base contract and exactly one hook. Asserting both
// here means dropping a method is a compile error rather than a hook that
// silently stops firing: the framework discovers hooks by type assertion, so a
// renamed method would simply never be called.
var (
	_ collage.Plugin          = (*stamp)(nil)
	_ collage.AfterRenderHook = (*stamp)(nil)
)

// Name identifies the plugin to the framework's plugin registry.
func (s *stamp) Name() string { return "blog-stamp" }

// Version reports the plugin's own version, for diagnostics.
func (s *stamp) Version() string { return "1.0.0" }

// Init takes the logger off the Host and registers the plugin's CLI subcommand.
// It runs once, at startup, before the first request is served.
//
// host is a collage.Host, not the *collage.App: a plugin can reach the pages, the
// logger, tag invalidation, and command registration, and nothing else.
func (s *stamp) Init(_ context.Context, host collage.Host) error {
	s.logger = host.Logger()

	return host.RegisterCommand(collage.Command{
		Name:  "posts",
		Usage: "posts",
		Short: "Count the posts this blog serves",
		Run: func(_ context.Context, _ []string) error {
			_, err := fmt.Printf("blog: %d posts, %d pages\n", len(s.store.List()), len(host.Pages()))
			return err
		},
	})
}

// Shutdown releases what Init acquired. This plugin holds nothing that needs
// releasing, so it only records that it stopped.
func (s *stamp) Shutdown(_ context.Context) error {
	s.logger.Debug("blog: stamp plugin shut down")
	return nil
}

// OnAfterRender injects StampMarker in front of the page's closing </body> tag,
// or appends it when the page has none — the global error pages render without
// the site layout, so they are a complete document with no body of their own to
// close.
//
// Replacing ev.HTML is the supported way to post-process a render: the replaced
// bytes are what the handler serves and what the cache stores. Two consequences
// follow, and both are real rather than incidental:
//
//   - a response served from cache does not run this hook again — the marker is
//     already baked into the cached bytes;
//   - a rendered error page does not run it at all, since the error path renders
//     the error page directly rather than through the request's render pipeline.
func (s *stamp) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	marker := []byte(StampMarker)

	if index := bytes.LastIndex(ev.HTML, bodyClose); index >= 0 {
		stamped := make([]byte, 0, len(ev.HTML)+len(marker))
		stamped = append(stamped, ev.HTML[:index]...)
		stamped = append(stamped, marker...)
		stamped = append(stamped, ev.HTML[index:]...)
		ev.HTML = stamped
		return nil
	}

	// A fresh slice rather than append(ev.HTML, ...): the event's slice belongs
	// to the render that produced it, and writing into its spare capacity would
	// be reaching into somebody else's buffer.
	stamped := make([]byte, 0, len(ev.HTML)+len(marker))
	stamped = append(stamped, ev.HTML...)
	ev.HTML = append(stamped, marker...)
	return nil
}

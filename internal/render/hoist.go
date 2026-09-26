package render

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	htmltemplate "html/template"

	"github.com/Elagoht/collage/internal/types"
)

// A {{hoist "head"}} writes a marker, not content.
//
// It has to: fragments render depth-first, so by the time a layout's <head> is
// written, nothing below it has run and there is nothing to write. The marker holds
// the place until the tree is finished, and resolveHoists fills it in.
//
// The token is random per render rather than a fixed string. A page that renders
// user-supplied text containing the marker would otherwise have that text replaced
// by the page's own stylesheets — an injection with a comment for a trigger.

// hoistMarker builds the placeholder for one area.
func hoistMarker(token, area string) string {
	return "<!--collage:hoist:" + token + ":" + area + "-->"
}

// newHoistToken returns the per-render marker token.
//
// A failure to read randomness is not worth failing a render over — the fallback is
// a fixed token, which costs the collision protection and keeps the page. The
// condition it guards against is a page that renders attacker-controlled text
// *and* a process whose randomness has failed, and the second of those has larger
// problems.
func newHoistToken() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "static"
	}
	return hex.EncodeToString(buf[:])
}

// hoistFunc is the "hoist" template function for one render.
func hoistFunc(token string) func(string) htmltemplate.HTML {
	return func(area string) htmltemplate.HTML {
		return htmltemplate.HTML(hoistMarker(token, area)) // any: a marker this package wrote, replaced before it reaches a client
	}
}

// markedAreas returns the areas html holds a marker for.
func markedAreas(html []byte, token string) map[string]bool {
	prefix := []byte("<!--collage:hoist:" + token + ":")
	areas := make(map[string]bool)
	for {
		at := bytes.Index(html, prefix)
		if at < 0 {
			return areas
		}
		tail := html[at+len(prefix):]
		end := bytes.Index(tail, []byte("-->"))
		if end < 0 {
			return areas
		}
		areas[string(tail[:end])] = true
		html = tail[end+3:]
	}
}

// resolveHoists replaces every marker in html with what the fragments declared.
//
// Run once, on the finished tree, so a declaration made anywhere below a marker
// still reaches it. An area nothing declared for resolves to nothing rather than
// being left in place: a marker on the wire is a comment that leaks the mechanism
// and, worse, one that a later render could mistake for its own.
func resolveHoists(html []byte, token string, hoisted *types.Hoisted) []byte {
	out, _ := resolveHoistsAt(html, token, hoisted)
	return out
}

// resolveHoistsAt is resolveHoists, also reporting where in the result each area's
// content ends — at its first marker — so a plugin running after the render can
// add to an area in the place the layout chose. See plugin.AfterRenderEvent.Hoist.
func resolveHoistsAt(html []byte, token string, hoisted *types.Hoisted) ([]byte, map[string]int) {
	if len(html) == 0 {
		return html, nil
	}

	prefix := []byte("<!--collage:hoist:" + token + ":")
	if !bytes.Contains(html, prefix) {
		return html, nil
	}

	ends := make(map[string]int)
	out := make([]byte, 0, len(html))
	rest := html
	for {
		at := bytes.Index(rest, prefix)
		if at < 0 {
			return append(out, rest...), ends
		}
		out = append(out, rest[:at]...)

		tail := rest[at+len(prefix):]
		end := bytes.Index(tail, []byte("-->"))
		if end < 0 {
			// An unterminated marker is not a marker. Copying it through is the
			// only thing that cannot make the page worse.
			return append(out, rest[at:]...), ends
		}
		area := string(tail[:end])
		out = append(out, hoisted.HTML(area)...)
		if _, seen := ends[area]; !seen {
			ends[area] = len(out)
		}
		rest = tail[end+3:]
	}
}

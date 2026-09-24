package types

import (
	"fmt"
	"html"
	"html/template"
)

// HeadArea is the hoist area the Hoist* helpers write to, and the one a layout
// places with {{hoist "head"}} inside its <head>.
const HeadArea = "head"

// BindAssets gives rc — and every copy of it the render makes — the resolver
// behind Asset. The render engine calls it; an application has no reason to.
func BindAssets(rc *RenderContext, resolve func(urlPath string) (string, error)) {
	if rc != nil && rc.state != nil {
		rc.state.assets = resolve
	}
}

// Asset returns a mounted file's content-addressed URL — what {{asset}} renders
// in a template — for a data handler that needs it in Go:
//
//	href, err := rc.Asset("/static/gallery.css")
//
// It returns ErrUnknownAsset for a path no mount serves.
func (rc *RenderContext) Asset(urlPath string) (string, error) {
	if rc.state == nil || rc.state.assets == nil {
		return "", fmt.Errorf("%w: %q: no mounts are known to this render", ErrUnknownAsset, urlPath)
	}
	resolved, err := rc.state.assets(urlPath)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %w", ErrUnknownAsset, urlPath, err)
	}
	return resolved, nil
}

// The Hoist* helpers declare the head elements nearly every page needs, escaping
// what they are given and choosing the key, so a more specific fragment's
// declaration replaces a less specific one's. Hoist itself inserts HTML exactly as
// written, which is right for what it is for and an XSS for a title built from a
// post's name.

// HoistTitle declares the page's <title>. The innermost declaration wins.
func (rc *RenderContext) HoistTitle(text string) {
	rc.Hoist(HeadArea, "title", template.HTML("<title>"+html.EscapeString(text)+"</title>"))
}

// HoistMeta declares <meta name="name" content="content">, one per name —
// description, robots, twitter:card.
func (rc *RenderContext) HoistMeta(name, content string) {
	rc.Hoist(HeadArea, "meta:"+name, template.HTML(`<meta name="`+html.EscapeString(name)+
		`" content="`+html.EscapeString(content)+`">`))
}

// HoistProperty declares <meta property="property" content="content">, one per
// property — the form Open Graph uses: og:title, og:image.
func (rc *RenderContext) HoistProperty(property, content string) {
	rc.Hoist(HeadArea, "property:"+property, template.HTML(`<meta property="`+html.EscapeString(property)+
		`" content="`+html.EscapeString(content)+`">`))
}

// HoistLink declares <link rel="rel" href="href">, one per rel — canonical,
// icon, manifest. For a rel a page carries several of, such as alternate, use
// Hoist with a key of your own.
func (rc *RenderContext) HoistLink(rel, href string) {
	rc.Hoist(HeadArea, "link:"+rel, template.HTML(`<link rel="`+html.EscapeString(rel)+
		`" href="`+html.EscapeString(href)+`">`))
}

// HoistAlternate declares <link rel="alternate" hreflang="hreflang" href="href">,
// one per language — what tells a search engine the page's translations. Pair it
// with {{localeURL}} or App.URL for each locale the page exists in.
func (rc *RenderContext) HoistAlternate(hreflang, href string) {
	rc.Hoist(HeadArea, "alternate:"+hreflang, template.HTML(`<link rel="alternate" hreflang="`+
		html.EscapeString(hreflang)+`" href="`+html.EscapeString(href)+`">`))
}

// HoistStylesheet declares a stylesheet for the page's head, by its mounted path,
// linked through its content-addressed URL. A stylesheet declared by several
// fragments appears once.
func (rc *RenderContext) HoistStylesheet(urlPath string) error {
	href, err := rc.Asset(urlPath)
	if err != nil {
		return err
	}
	rc.Hoist(HeadArea, "stylesheet:"+urlPath, template.HTML(`<link rel="stylesheet" href="`+html.EscapeString(href)+`">`))
	return nil
}

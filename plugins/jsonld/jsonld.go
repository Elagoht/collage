// Package jsonld is a collage plugin that emits schema.org structured data into the
// document head.
//
//	app, err := collage.New(&collage.Config{
//		Plugins: []collage.Plugin{jsonld.New()},
//	})
//
// A page contributes its own structured data from a data handler, by putting nodes
// into the render's shared data:
//
//	func articleData(ctx context.Context, rc *collage.RenderContext) (any, []string, error) {
//		article, err := client.Article(ctx, rc.Param("slug"))
//		// ...
//		jsonld.Emit(rc, jsonld.Article{
//			Headline:      article.Title,
//			DatePublished: article.PublishedAt,
//			AuthorName:    article.Author,
//		})
//		return view{Article: article}, nil, nil
//	}
//
// The plugin reads them after the render and writes one <script> tag per node. It
// reads the render's data rather than the rendered HTML deliberately: the data
// handler already fetched the article, and parsing a headline back out of the markup
// to describe the markup is both slower and wrong the first time a template changes.
package jsonld

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Elagoht/collage/pkg/collage"
)

// Name is the plugin's name, and the key its configuration is found under.
const Name = "elagoht/jsonld"

// SharedDataKey is where Emit stores nodes in a render's shared data. It is
// exported so an application that would rather not import this package into its
// data handlers can write the same key itself.
const SharedDataKey = "jsonld.nodes"

// Emit adds nodes to the structured data for the render rc belongs to.
//
// It appends rather than replaces, so an article page's fragment and a breadcrumb
// fragment can each contribute without knowing about the other.
func Emit(rc *collage.RenderContext, nodes ...Node) {
	if rc == nil || len(nodes) == 0 {
		return
	}
	existing, _ := rc.Get(SharedDataKey)
	collected, _ := existing.([]Node) // any: SharedData's own value type
	rc.Set(SharedDataKey, append(collected, nodes...))
}

// Config is the plugin's configuration.
type Config struct {
	// SiteName and SiteURL describe the site. When SiteName is set, a WebSite node
	// is emitted on every page that has no WebSite of its own.
	SiteName string `json:"siteName"`
	SiteURL  string `json:"siteURL"`
	// SearchURL is a search endpoint with "{query}" where the term goes. It is
	// emitted as part of the WebSite node, and ignored without SiteName.
	SearchURL string `json:"searchURL"`
	// Disabled turns the plugin off without removing it from the application, so
	// structured data can be switched off per deployment.
	Disabled bool `json:"disabled"`
}

// Plugin emits structured data.
type Plugin struct {
	cfg Config
	log *slog.Logger
}

// New returns a plugin configured entirely from the application.
func New() *Plugin { return &Plugin{} }

// NewWith returns a plugin with cfg as its starting point, which the application's
// own configuration is then decoded over.
func NewWith(cfg Config) *Plugin { return &Plugin{cfg: cfg} }

func (p *Plugin) Name() string    { return Name }
func (p *Plugin) Version() string { return "1.0.0" }

func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	p.log = host.Logger()
	return host.Config(&p.cfg)
}

func (p *Plugin) Shutdown(context.Context) error { return nil }

// headClose is where the script tags go. Structured data belongs in the head, and
// putting it last in the head keeps it out of the way of anything the page needs
// sooner.
var headClose = []byte("</head>")

// OnAfterRender writes the render's structured data into the document head.
//
// A page with nothing to say emits nothing at all — not an empty script tag. A
// consumer treats an empty or unparseable block as a reason to distrust the page,
// so silence is the better answer.
func (p *Plugin) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	if p.cfg.Disabled {
		return nil
	}

	nodes := p.nodesFor(ev)
	if len(nodes) == 0 {
		return nil
	}

	insertAt := bytes.Index(ev.HTML, headClose)
	if insertAt < 0 {
		// No head to write into. This is a page whose layout does not produce one
		// — an error page stripped to the bone, say — and inventing a place for
		// the data is worse than not emitting it.
		return nil
	}

	var block bytes.Buffer
	for _, node := range nodes {
		// json.Marshal, not an Encoder with SetEscapeHTML(false): Marshal escapes
		// "<", ">" and "&" to their \u form, and that is what keeps a node safe
		// inside a <script> element. The HTML parser ends a script at the first
		// "</script" in its text, wherever it appears — including inside a JSON
		// string — so a headline containing one would close the block early and
		// spill the rest of the JSON into the document as markup.
		//
		// The escaping is held twice over, which is worth knowing before changing
		// either half: each node's own MarshalJSON escapes when it marshals its
		// wire struct, and Marshal re-escapes a Marshaler's output when it
		// compacts it. Disabling one layer changes nothing; disabling both opens
		// the hole, which is what
		// TestPlugin_ClosingScriptTagInContentCannotEscape demonstrates.
		encoded, err := json.Marshal(node)
		if err != nil {
			// One bad node must not cost the page its other nodes, or the page
			// itself. Structured data is an enhancement; a 500 for a malformed
			// one would be the plugin deciding otherwise on the site's behalf.
			if p.log != nil {
				p.log.Warn("jsonld: node could not be marshalled", "type", node.Type(), "err", err)
			}
			continue
		}
		block.WriteString(`<script type="application/ld+json">`)
		block.Write(encoded)
		block.WriteString("</script>")
	}
	if block.Len() == 0 {
		return nil
	}

	out := make([]byte, 0, len(ev.HTML)+block.Len())
	out = append(out, ev.HTML[:insertAt]...)
	out = append(out, block.Bytes()...)
	out = append(out, ev.HTML[insertAt:]...)
	ev.HTML = out
	return nil
}

// nodesFor collects what the render contributed, plus the site-wide WebSite node
// when the application configured one and the page did not supply its own.
func (p *Plugin) nodesFor(ev *collage.AfterRenderEvent) []Node {
	var nodes []Node
	if raw, ok := ev.Data[SharedDataKey]; ok {
		if collected, ok := raw.([]Node); ok { // any: SharedData's own value type
			nodes = append(nodes, collected...)
		}
	}

	if p.cfg.SiteName == "" {
		return nodes
	}
	for _, node := range nodes {
		if node.Type() == "WebSite" {
			return nodes
		}
	}
	return append(nodes, WebSite{Name: p.cfg.SiteName, URL: p.cfg.SiteURL, SearchURL: p.cfg.SearchURL})
}

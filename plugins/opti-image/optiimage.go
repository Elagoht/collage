// Package optiimage is a collage plugin that rewrites images declaring a width and
// a height to resized copies it serves itself.
//
//	app, err := collage.New(&collage.Config{
//		Plugins: []collage.Plugin{optiimage.New()},
//		PluginConfig: cfg, // must name at least one allowed origin
//	})
//
// It has to be supplied through Config.Plugins: it registers the route it serves
// its own images from, and that has to happen while the application is built.
//
// # What it does, and when
//
// The rewrite happens during the render: an <img> with width, height and a src on
// an allowed origin has its src replaced with a signed URL under the plugin's own
// prefix. Nothing is fetched at that point.
//
// The fetch, the decode and the resize happen on the first request for that URL.
// Doing them during the render would make the first view of a page as slow as its
// slowest image, and would do it while holding a render the reader is waiting on.
// Doing them at the image endpoint means the page arrives at once and the images
// arrive as the browser asks for them, which is also the order the browser wants.
//
// # Why the URLs are signed
//
// The image endpoint takes a URL from the request and fetches it. Unsigned, that is
// a server-side request forgery primitive reachable by anyone who can read the
// page's HTML and edit a query string. The allowlist is checked again at fetch
// time, so the signature is not the only defence — it is the one that stops an
// attacker probing the allowlist at all, and stops the cache becoming unbounded
// storage keyed on strings they choose.
package optiimage

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	// The decoders the standard library has. They register themselves with
	// image.Decode, which is how a JPEG is told from a PNG without trusting the
	// origin's Content-Type — which is a claim, not a fact.
	_ "image/gif"

	"github.com/Elagoht/collage/pkg/collage"
)

// Plugin rewrites and serves optimised images.
type Plugin struct {
	cfg    Config
	signer *signer
	client *http.Client
	log    *slog.Logger
}

// New returns a plugin configured entirely from the application. It does nothing
// until an allowed origin is configured.
func New() *Plugin { return &Plugin{} }

// NewWith returns a plugin with cfg as its starting point, which the application's
// own configuration is decoded over.
func NewWith(cfg Config) *Plugin { return &Plugin{cfg: cfg} }

func (p *Plugin) Name() string    { return Name }
func (p *Plugin) Version() string { return "1.0.0" }

// Configure decodes the configuration and prepares the signer and the cache. It
// does not register the route: that needs Host, which Init receives.
func (p *Plugin) Configure(_ context.Context, host collage.ConfigHost) error {
	p.log = host.Logger()
	if err := host.Config(&p.cfg); err != nil {
		return err
	}
	p.cfg = p.cfg.withDefaults()
	if err := p.cfg.validate(); err != nil {
		return err
	}

	signer, err := newSigner()
	if err != nil {
		return err
	}
	p.signer = signer
	p.client = &http.Client{Timeout: p.cfg.FetchTimeout}
	return nil
}

// imageTTL is how long a client and the framework's cache may keep an optimised
// image. The URL names the source and the size and is signed, so its content cannot
// change without the URL changing — which makes a long life correct rather than
// merely convenient.
const imageTTL = 30 * 24 * time.Hour

// Init registers the routes the optimised images are served from: one per output
// format.
//
// One per format, because a document declares a single content type for every
// response it serves, and these are JPEG or PNG depending on what the source is.
// Putting the format in the path rather than the token is what lets each route
// declare the truth about its own responses.
//
// Documents rather than a mount: a mount serves a filesystem, and these images do
// not exist until they are asked for. A document is the framework's name for a
// routed response whose body a handler produces.
//
// Incremental rather than Dynamic, so the framework caches the bytes and answers
// conditional requests, and so the response carries a real max-age. Dynamic would
// mean "never stored", and an image re-fetched and re-resized on every request is
// the opposite of what this plugin is for. The images share the application's page
// cache: a site with many of them should say so in Cache.MaxEntries.
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	if p.log == nil {
		p.log = host.Logger()
	}
	if !p.active() {
		// Configured with no origins, or disabled. Registering the routes anyway
		// would claim URL space for handlers that refuse everything.
		return nil
	}

	for _, format := range p.formats() {
		doc := collage.NewDocument("opti-image-"+format.name, format.contentType).
			WithPath("en", p.cfg.Prefix+format.name+"/{token}").
			WithHandler(p.handlerFor(format)).
			Incremental(imageTTL).
			// The token is the whole request; nothing in a query string changes
			// what this route produces, so nothing in one belongs in its key.
			WithCacheParams().
			Build()
		if err := host.RegisterDocument(doc); err != nil {
			return err
		}
	}
	return nil
}

// outputFormat is one format the plugin can serve.
type outputFormat struct {
	name        string
	contentType string
}

var (
	formatJPEG = outputFormat{name: "jpeg", contentType: "image/jpeg"}
	formatPNG  = outputFormat{name: "png", contentType: "image/png"}
	formatWebP = outputFormat{name: "webp", contentType: "image/webp"}
)

// formats returns the formats this build can produce.
func (p *Plugin) formats() []outputFormat {
	formats := []outputFormat{formatJPEG, formatPNG}
	if p.cfg.WebP && webpEncoder != nil {
		formats = append(formats, formatWebP)
	}
	return formats
}

// formatFor picks the output format for a source URL.
//
// From the source's extension, because that is the only thing known at rewrite
// time — the image itself is not fetched until someone asks for it. A PNG stays a
// PNG: re-encoding one as JPEG drops its transparency and puts a black rectangle
// where the page expected to see through.
func (p *Plugin) formatFor(source string) outputFormat {
	if p.cfg.WebP && webpEncoder != nil {
		return formatWebP
	}
	switch {
	case strings.HasSuffix(strings.ToLower(source), ".png"),
		strings.HasSuffix(strings.ToLower(source), ".gif"):
		return formatPNG
	default:
		return formatJPEG
	}
}

func (p *Plugin) Shutdown(context.Context) error { return nil }

// active reports whether the plugin has anything to do.
func (p *Plugin) active() bool {
	return !p.cfg.Disabled && len(p.cfg.AllowedOrigins) > 0 && p.signer != nil
}

// OnAfterRender rewrites the page's images.
func (p *Plugin) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	if !p.active() {
		return nil
	}
	ev.HTML = p.rewriteImages(ev.HTML)
	return nil
}

// handlerFor returns the document handler for one output format.
func (p *Plugin) handlerFor(format outputFormat) collage.DocumentHandlerFunc {
	return func(ctx context.Context, rc *collage.RenderContext) ([]byte, []string, error) {
		tok, err := p.signer.decode(rc.Param("token"))
		if err != nil {
			// Not found rather than forbidden: a signature this process did not
			// issue names nothing, and 404 says so without confirming to a prober
			// that the shape of their guess was close.
			return nil, nil, fmt.Errorf("%w: %v", collage.ErrNotFound, err)
		}

		body, err := p.produce(ctx, tok, format)
		if err != nil {
			p.log.Warn("opti-image: could not produce image",
				"source", tok.Source, "width", tok.Width, "height", tok.Height, "err", err)
			return nil, nil, err
		}
		return body, nil, nil
	}
}

// produce fetches, decodes, resizes and re-encodes one image.
func (p *Plugin) produce(ctx context.Context, tok token, format outputFormat) ([]byte, error) {
	source, allowed := p.cfg.allows(tok.Source)
	if !allowed {
		// Re-checked even though the signature proves this plugin issued the URL:
		// the allowlist may have been narrowed since, and a signed URL must not
		// outlive the permission it was issued under.
		return nil, fmt.Errorf("opti-image: %q is not an allowed origin", tok.Source)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("opti-image: origin answered %s", resp.Status)
	}

	// Limited before reading, not after: a body with no Content-Length, or a lying
	// one, is exactly the case a limit exists for.
	body, err := io.ReadAll(io.LimitReader(resp.Body, p.cfg.MaxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > p.cfg.MaxSourceBytes {
		return nil, fmt.Errorf("opti-image: source is larger than %d bytes", p.cfg.MaxSourceBytes)
	}

	// The header is decoded first so an enormous bitmap is refused before it is
	// allocated. A small file can decode to gigabytes — the decompression bomb —
	// and the byte limit above cannot see it.
	config, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("opti-image: not a decodable image: %w", err)
	}
	if config.Width*config.Height > p.cfg.MaxPixels {
		return nil, fmt.Errorf("opti-image: source is %dx%d, over the %d pixel limit",
			config.Width, config.Height, p.cfg.MaxPixels)
	}

	decoded, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("opti-image: decode: %w", err)
	}

	return p.encode(resize(decoded, tok.Width, tok.Height), format)
}

// encode writes the resized image in the format the route promised.
//
// A GIF becomes a PNG, because the standard library decodes GIF and encodes PNG,
// and one still frame in a lossless format is the honest outcome — the alternative
// is a broken animation or a dependency.
func (p *Plugin) encode(img image.Image, format outputFormat) ([]byte, error) {
	var buf bytes.Buffer

	switch format {
	case formatWebP:
		if err := encodeWebP(&buf, img, p.cfg.Quality); err != nil {
			return nil, err
		}
	case formatPNG:
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
	default:
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: p.cfg.Quality}); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

# imns/opti-image

A collage plugin that rewrites images declaring a `width` and a `height` to resized
copies it serves itself.

```go
app, err := collage.New(&collage.Config{
	Plugins:      []collage.Plugin{optiimage.New()},
	PluginConfig: cfg,
})
```

It must be supplied through `Config.Plugins`: it registers the routes it serves
images from, and that happens while the application is built.

## Configuration

```json
{
  "imns/opti-image": {
    "allowedOrigins": [
      { "scheme": "https", "host": "images.example.com" }
    ],
    "prefix": "/_image/",
    "maxSourceBytes": 8388608,
    "maxPixels": 40000000,
    "fetchTimeout": "10s",
    "quality": 82,
    "webp": false
  }
}
```

**An empty `allowedOrigins` disables the plugin.** It never means "any host". A
fetcher that defaults to fetching anything is a server-side request forgery
primitive wearing a feature's name.

The scheme is part of an origin, not decoration: allowing a host without saying
which scheme permits a plaintext fetch of an image the page serves over TLS.

## Only declared-size images

An `<img>` is rewritten when it carries both `width` and `height` as pixel counts.
That is the contract, not a simplification:

- the declared size is the only statement of how large the image will be drawn, so
  without it any target size is a guess, and a guessed resize is worse than the
  original at every guess but one;
- the page already reserves the right space, so nothing reflows when the smaller
  file arrives;
- `width="100%"` is not a pixel count and is left alone.

`data-src` is left alone too — it contains `src=`, and rewriting it would break the
lazy-loading script the page uses while leaving the real source untouched.

## Nothing is fetched during the render

The render only rewrites the URL. The fetch, the decode and the resize happen on the
first request for that URL.

Doing the work during the render would make the first view of a page as slow as its
slowest image, while a reader waits on it. At the image endpoint the page arrives at
once and the images arrive as the browser asks for them — which is the order the
browser wanted them in anyway. An origin that is down costs its image and not the
page.

## Why the URLs are signed

The image endpoint takes a source from the request and fetches it. The allowlist is
re-checked at that point, so a token naming some other host is refused whether or
not it is signed.

What the HMAC alone prevents is a *valid* token naming an allowed origin at any size
the requester likes. Without it, anyone who can read the page can request a
9999×9999 resize of every image on the site, as many distinct sizes as they care to
type — each one a fetch, a decode, a resize and a cache entry. That is a denial of
service built entirely out of legitimate-looking requests, and
`TestPlugin_SignatureStopsForgedWorkAgainstAnAllowedOrigin` is what notices when the
check goes away.

The key is generated per process. A restart invalidates outstanding URLs, which
costs a re-render of pages still held downstream and buys a key nobody has to store,
rotate or keep out of a configuration file. **A fleet behind a load balancer wants a
shared key**, and `Config` would have to grow one before this is deployed that way.

## Limits

`maxSourceBytes` bounds what is read. `maxPixels` is checked from the image header
*before* anything is decoded, because a small file can decode to an enormous bitmap
— the decompression bomb — and a byte limit cannot see it coming.

## Output format

Chosen from the source's extension, because the image is not fetched at rewrite
time. A PNG or GIF becomes a PNG; everything else becomes a JPEG. A PNG is not
re-encoded as JPEG: that drops its transparency and puts a black rectangle where the
page expected to see through.

Each format is its own route, because a collage document declares one content type
for every response it serves — so "which format" has to be part of which route.

Resizing is area averaging, standard library only. Nearest-neighbour downscaling is
where aliasing comes from: a one-pixel line either survives whole or vanishes, so a
photograph of a building acquires moiré and a screenshot of text becomes unreadable.
The averaging is done on premultiplied values, or a transparent black pixel beside
an opaque white one averages to translucent grey and a downscaled logo acquires a
dark halo.

## WebP

The standard library has no WebP encoder and every working one needs cgo. A plugin
that cannot be built without a C toolchain is a plugin most people cannot use, so
the default build has none and `"webp": true` does nothing on its own.

To get it, build with `-tags webp` and register an encoder:

```go
//go:build webp

func init() { optiimage.RegisterWebPEncoder(myCgoEncoder{}) }
```

The tag makes the dependency opt-in; the registration makes it replaceable.

## Caching

The images are documents with a 30-day `Incremental` strategy, so the framework
caches the bytes, answers conditional requests and sends a real `max-age`. The URL
names the source and the size and is signed, so its content cannot change without
the URL changing — which is what makes a long life correct rather than convenient.

They share the application's page cache. A site with many images should say so in
`Cache.MaxEntries`.

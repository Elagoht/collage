package optiimage

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Name is the plugin's name, and the key its configuration is found under.
const Name = "imns/opti-image"

// Origin is one host the plugin is allowed to fetch from.
//
// Scheme is part of it, not an afterthought: allowing "example.com" without saying
// which scheme allows a plaintext fetch of an image the page will serve over TLS,
// which is a mixed-content warning at best and a tampering opportunity at worst.
type Origin struct {
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
}

// Config is the plugin's configuration.
type Config struct {
	// AllowedOrigins is the set of hosts images may be fetched from. An empty list
	// disables the plugin entirely rather than allowing everything: a fetcher that
	// defaults to "any host" is an SSRF primitive wearing a feature's name.
	AllowedOrigins []Origin `json:"allowedOrigins"`
	// Prefix is the URL prefix optimised images are served under. It must begin
	// and end with "/". Defaults to "/_image/".
	Prefix string `json:"prefix"`
	// MaxSourceBytes refuses an origin image larger than this. Defaults to 8 MiB.
	// Without it, one hostile or careless origin can exhaust the process's memory.
	MaxSourceBytes int64 `json:"maxSourceBytes"`
	// MaxPixels refuses an origin image with more pixels than this, before it is
	// decoded. Defaults to 40 million. A small file can decode to an enormous
	// bitmap — the decompression bomb — and the byte limit above does not see it.
	MaxPixels int `json:"maxPixels"`
	// FetchTimeout bounds a single origin fetch. Defaults to 10s.
	FetchTimeout time.Duration `json:"fetchTimeout"`
	// CacheBytes caps the memory held by resized images. Defaults to 64 MiB.
	CacheBytes int64 `json:"cacheBytes"`
	// Quality is the JPEG quality of re-encoded images, 1..100. Defaults to 82.
	Quality int `json:"quality"`
	// WebP re-encodes to WebP where an encoder is available. The default build
	// has none — encoding WebP needs cgo — so this does nothing unless the binary
	// was built with the "webp" tag and an encoder registered. See webp.go.
	WebP bool `json:"webp"`
	// Disabled turns the plugin off without removing it from the application.
	Disabled bool `json:"disabled"`
}

// withDefaults returns cfg with every unset field filled in.
func (c Config) withDefaults() Config {
	if c.Prefix == "" {
		c.Prefix = "/_image/"
	}
	if c.MaxSourceBytes <= 0 {
		c.MaxSourceBytes = 8 << 20
	}
	if c.MaxPixels <= 0 {
		c.MaxPixels = 40_000_000
	}
	if c.FetchTimeout <= 0 {
		c.FetchTimeout = 10 * time.Second
	}
	if c.CacheBytes <= 0 {
		c.CacheBytes = 64 << 20
	}
	if c.Quality <= 0 || c.Quality > 100 {
		c.Quality = 82
	}
	return c
}

// validate reports a configuration that cannot work.
func (c Config) validate() error {
	if !strings.HasPrefix(c.Prefix, "/") || !strings.HasSuffix(c.Prefix, "/") {
		return fmt.Errorf("opti-image: prefix %q must begin and end with \"/\"", c.Prefix)
	}
	if c.Prefix == "/" {
		return fmt.Errorf("opti-image: prefix must not be \"/\", which would claim the whole site")
	}
	for _, origin := range c.AllowedOrigins {
		if origin.Host == "" {
			return fmt.Errorf("opti-image: an allowed origin has no host")
		}
		if origin.Scheme != "http" && origin.Scheme != "https" {
			return fmt.Errorf("opti-image: origin %q has scheme %q, want http or https", origin.Host, origin.Scheme)
		}
	}
	return nil
}

// allows reports whether raw names an image this plugin may fetch.
//
// The comparison is on the parsed URL's scheme and host, never on the string: a
// string check is defeated by "https://allowed.example@evil.example/x", whose host
// is evil.example and whose prefix is the allowed one. The port is part of the host
// for this purpose, so allowing "images.example.com" does not allow
// "images.example.com:8080" — a different service on a different port is a
// different origin, whatever the certificate says.
func (c Config) allows(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return nil, false
	}
	for _, origin := range c.AllowedOrigins {
		if parsed.Scheme == origin.Scheme && parsed.Host == origin.Host {
			return parsed, true
		}
	}
	return nil, false
}

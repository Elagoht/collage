// Command thewire is a magazine built with collage.
//
// It is a separate module with its own module path, because that is what an
// application using the framework is: collage is a dependency here, not a parent.
// Everything it renders comes from a newsroom API it reads over HTTP.
//
// Start the backend first, then the site:
//
//	cd ../newsroom-api && go run .    # serves JSON on localhost:8080
//	go run .                          # serves the site on localhost:3000
//
// Both take flags; both read the same settings from the environment. To watch the
// site degrade, start the backend with failure injection:
//
//	cd ../newsroom-api && go run . -fail-every 3 -latency 400ms
//
// What it exercises, beyond what examples/blog covers:
//
//   - five page types over one layout, with per-locale paths — "/category/climate"
//     in English, "/tr/kategori/climate" in Turkish;
//   - a section nav and a "most read" sidebar that are not required and have
//     fallbacks, so an unreachable backend costs the furniture and not the article;
//   - a document head filled per page through a slot, because a layout cannot see
//     the headline its content fragment has yet to fetch;
//   - an article fragment that is required, so a missing piece is a 404 rather than
//     chrome wrapped around a hole;
//   - a search page that is deliberately never cached, because its cache key
//     includes the raw query string;
//   - "/rss.xml", "/sitemap.xml", "/robots.txt" and "/healthz" as documents;
//   - "/static/" as a mounted embed.FS, outside the page cache entirely.
//
// Templates and stylesheet are embedded, so the binary runs from any directory.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

func main() {
	host := flag.String("host", env("HOST", "localhost"), "interface to listen on (env HOST)")
	port := flag.Int("port", envInt("PORT", 3000), "port to listen on (env PORT)")
	apiURL := flag.String("api", env("MAGAZINE_API_URL", "http://localhost:8080"), "newsroom API base URL (env MAGAZINE_API_URL)")
	publicURL := flag.String("public-url", env("PUBLIC_BASE_URL", ""), "public origin for feed and sitemap links; defaults to http://host:port (env PUBLIC_BASE_URL)")
	cacheTTL := flag.Duration("cache-ttl", envDuration("CACHE_TTL", 5*time.Minute), "default page cache lifetime (env CACHE_TTL)")
	logLevel := flag.String("log-level", env("LOG_LEVEL", "info"), "debug, info, warn or error (env LOG_LEVEL)")
	pluginConfig := flag.String("plugins", env("PLUGINS_CONFIG", "plugins-config.json"), "plugin configuration file; a missing one means every plugin runs on its defaults (env PLUGINS_CONFIG)")
	devMode := flag.Bool("dev", envBool("DEV_MODE", false), "surface failed fragments as HTML comments (env DEV_MODE)")
	flag.Parse()

	log := newLogger(*logLevel)

	// A missing file is not an error: every plugin's defaults already describe what
	// "unconfigured" means, and a deployment that configures none should not have
	// to create an empty file to say so. This is also the one line that ties the
	// site to JSON — the framework takes a map and does not care where it came
	// from, so swapping this for YAML or the environment changes nothing else.
	plugins, err := collage.LoadPluginConfig(*pluginConfig)
	if err != nil {
		log.Error("site: plugin configuration is unusable", "path", *pluginConfig, "err", err)
		os.Exit(1)
	}

	cfg := config{
		Host:          *host,
		Port:          *port,
		APIBaseURL:    *apiURL,
		PublicBaseURL: *publicURL,
		CacheTTL:      *cacheTTL,
		DevMode:       *devMode,
		Logger:        log,
		PluginConfig:  plugins,
	}
	if cfg.PublicBaseURL == "" {
		cfg.PublicBaseURL = "http://" + *host + ":" + strconv.Itoa(*port)
	}

	app, client, err := newSite(cfg)
	if err != nil {
		log.Error("site: cannot start", "err", err)
		os.Exit(1)
	}

	// The backend is checked once at startup and reported, not required. A site
	// that refuses to start because its backend is down cannot serve the error
	// page explaining that its backend is down, and it cannot come up first during
	// a cold start of the whole stack.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := client.Health(ctx); err != nil {
		log.Warn("site: newsroom API is not answering; pages will render degraded", "api", cfg.APIBaseURL, "err", err)
	} else {
		log.Info("site: newsroom API reachable", "api", cfg.APIBaseURL)
	}
	cancel()

	// No "starting" line here: ListenAndServe logs "collage: listening" once the
	// port is actually bound, which is the only moment the claim is true.
	if err := app.ListenAndServe(); err != nil {
		log.Error("site: stopped with an error", "err", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

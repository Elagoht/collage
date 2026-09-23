// Command site is the magazine example: a news site built on collage, reading
// everything it renders from the fake API in ../api over HTTP.
//
// It is the framework's production-shaped example. Where examples/blog shows the
// mechanisms one at a time against an in-process store, this one puts them together
// against a backend that can be slow, can fail, and is on the other end of a socket:
//
//   - five page types over one layout, with per-locale paths — "/category/climate"
//     in English, "/kategori/climate" in Turkish;
//   - a masthead nav and a "most read" sidebar that are not required and have
//     fallbacks, so an unreachable backend costs a reader the furniture and not the
//     article;
//   - an article fragment that is required, so a missing piece is a 404 rather than
//     chrome wrapped around a hole;
//   - a search page that is deliberately never cached, because its cache key
//     includes the raw query string;
//   - "/rss.xml", "/sitemap.xml" and "/robots.txt" as documents, marshalled with
//     encoding/xml rather than rendered through the HTML template engine;
//   - "/static/" as a mounted embed.FS, outside the page cache entirely;
//   - "/healthz", which reports whether the backend is reachable.
//
// Both binaries are single files with nothing beside them: templates, stylesheet and
// corpus are all embedded, so either runs from any working directory.
//
// Run it against the API:
//
//	go run ./examples/magazine/cmd/api &
//	go run ./examples/magazine/cmd/site
//
// To watch the degraded paths, start the API with failure injection:
//
//	go run ./examples/magazine/cmd/api -fail-every 3 -latency 400ms
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"strconv"
	"time"
)

func main() {
	host := flag.String("host", env("HOST", "localhost"), "interface to listen on (env HOST)")
	port := flag.Int("port", envInt("PORT", 3000), "port to listen on (env PORT)")
	apiURL := flag.String("api", env("MAGAZINE_API_URL", "http://localhost:8080"), "newsroom API base URL (env MAGAZINE_API_URL)")
	publicURL := flag.String("public-url", env("PUBLIC_BASE_URL", ""), "public origin for feed and sitemap links; defaults to http://host:port (env PUBLIC_BASE_URL)")
	cacheTTL := flag.Duration("cache-ttl", envDuration("CACHE_TTL", 5*time.Minute), "default page cache lifetime (env CACHE_TTL)")
	logLevel := flag.String("log-level", env("LOG_LEVEL", "info"), "debug, info, warn or error (env LOG_LEVEL)")
	devMode := flag.Bool("dev", envBool("DEV_MODE", false), "surface failed fragments as HTML comments (env DEV_MODE)")
	flag.Parse()

	log := newLogger(*logLevel)

	cfg := config{
		Host:          *host,
		Port:          *port,
		APIBaseURL:    *apiURL,
		PublicBaseURL: *publicURL,
		CacheTTL:      *cacheTTL,
		DevMode:       *devMode,
		Logger:        log,
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

// Command api is the magazine example's fake backend: a read-only JSON API over an
// embedded corpus of twenty articles.
//
// It exists so the site has something real to talk to. The site fetches over HTTP,
// decodes JSON, handles timeouts and retries, and degrades when this process is
// unwell — none of which it would exercise against an in-process slice.
//
// Two knobs make the site's failure handling demonstrable rather than theoretical:
//
//	-latency 800ms    # every response is slow
//	-fail-every 3     # every third request answers 503
//
// With those set, the site's "most read" sidebar drops out while the article still
// renders, which is the behaviour its non-required fragments exist to produce.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Elagoht/collage/examples/magazine/newsroom"
)

func main() {
	addr := flag.String("addr", env("MAGAZINE_API_ADDR", "localhost:8080"), "address to listen on (env MAGAZINE_API_ADDR)")
	latency := flag.Duration("latency", envDuration("MAGAZINE_API_LATENCY", 0), "artificial latency added to every response (env MAGAZINE_API_LATENCY)")
	failEvery := flag.Int("fail-every", envInt("MAGAZINE_API_FAIL_EVERY", 0), "answer 503 on every Nth request; 0 disables (env MAGAZINE_API_FAIL_EVERY)")
	logLevel := flag.String("log-level", env("LOG_LEVEL", "info"), "debug, info, warn or error (env LOG_LEVEL)")
	flag.Parse()

	log := newLogger(*logLevel)

	store, err := newsroom.NewStore()
	if err != nil {
		log.Error("api: corpus is unusable", "err", err)
		os.Exit(1)
	}

	chaos := &Chaos{Latency: *latency, FailEvery: *failEvery}
	if chaos.Latency > 0 || chaos.FailEvery > 0 {
		log.Warn("api: failure injection is on", "latency", chaos.Latency, "failEvery", chaos.FailEvery)
	}

	if err := run(*addr, newMux(store, log, chaos), log); err != nil {
		log.Error("api: stopped with an error", "err", err)
		os.Exit(1)
	}
}

// run binds the address, serves until SIGINT or SIGTERM, then drains.
//
// The listener is opened before anything is logged as started, so a port that is
// already taken is reported as a failure rather than announced as a success one
// line before the process exits.
func run(addr string, handler http.Handler, log *slog.Logger) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Info("api: listening", "addr", listener.Addr().String())

	errs := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	select {
	case err := <-errs:
		return err
	case sig := <-signals:
		log.Info("api: shutting down", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		log.Info("api: stopped")
		return nil
	}
}

// newLogger builds the structured logger both the request log and the lifecycle
// messages go through. Logging to stdout rather than stderr is deliberate: these
// are the process's output, not its diagnostics, and a container runtime that
// splits the two should file them accordingly.
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

package types

import (
	"context"
	"fmt"
)

// RenderStrategy selects how a page's output is cached and regenerated.
type RenderStrategy int

const (
	// StrategyAuto is the strategy of a page or document that declared none.
	// Registration resolves it: to StrategyDynamic when something in it fetches
	// per render — a data handler, a slot resolver, a document handler — and to
	// StrategyStatic when everything in it is fixed. A registered route never
	// carries it.
	StrategyAuto RenderStrategy = iota
	// StrategyDynamic renders on every request and never serves from cache.
	StrategyDynamic
	// StrategyStatic renders once and serves from cache until explicitly invalidated.
	StrategyStatic
	// StrategyIncremental serves from cache until the page's CacheTTL elapses.
	StrategyIncremental
)

// String returns the human-readable name of s: "auto", "dynamic", "static",
// "incremental", or "unknown(<n>)" for a value outside the declared range.
func (s RenderStrategy) String() string {
	switch s {
	case StrategyAuto:
		return "auto"
	case StrategyDynamic:
		return "dynamic"
	case StrategyStatic:
		return "static"
	case StrategyIncremental:
		return "incremental"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// Cacheable reports whether s serves rendered output from cache. It is true for
// StrategyStatic and StrategyIncremental, and false for StrategyDynamic and any
// out-of-range value.
func (s RenderStrategy) Cacheable() bool {
	return s == StrategyStatic || s == StrategyIncremental
}

// StaticParamsFunc returns, for locale, the path parameter values a static build
// writes a route for: one map per file, keyed by placeholder name —
// {"slug": "hello-world"} for "/blog/{slug}". Each map must fill the route's
// pattern in locale exactly, as a link built by name must.
type StaticParamsFunc func(ctx context.Context, locale string) ([]map[string]string, error)

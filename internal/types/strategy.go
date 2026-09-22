package types

import "fmt"

// RenderStrategy selects how a page's output is cached and regenerated.
type RenderStrategy int

const (
	// StrategyDynamic renders on every request and never serves from cache.
	StrategyDynamic RenderStrategy = iota
	// StrategyStatic renders once and serves from cache until explicitly invalidated.
	StrategyStatic
	// StrategyIncremental serves from cache until the page's CacheTTL elapses.
	StrategyIncremental
)

// String returns the human-readable name of s: "dynamic", "static", "incremental",
// or "unknown(<n>)" for a value outside the declared range.
func (s RenderStrategy) String() string {
	switch s {
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

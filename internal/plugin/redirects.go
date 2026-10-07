package plugin

import (
	"fmt"

	"github.com/Elagoht/collage/internal/types"
)

// BuiltRedirect is one redirect a static export carries to its host.
type BuiltRedirect struct {
	// From is the path pattern, as registered: "/old/{slug}".
	From string
	// To is the destination, as registered: "/new/{slug}" or an absolute URL.
	To string
	// Status is 301, 302, 307 or 308, or 410 for a path that is gone.
	Status int
	// Source names where the rule came from: "page:<name>",
	// "document:<name>" or a plugin's name.
	Source string
}

// RedirectSource is a plugin whose redirects a static export carries. The build
// asks it once, after every file is written, and hands its rules to
// BuildFinishedHook in BuildFinishedEvent.Redirects.
type RedirectSource interface {
	Redirects() []BuiltRedirect
}

// PluginRedirects collects the rules of every RedirectSource, in registration
// order, each stamped with its plugin's name and checked for control
// characters.
func (r *Registry) PluginRedirects() ([]BuiltRedirect, error) {
	var out []BuiltRedirect
	for _, p := range r.snapshot() {
		source, ok := p.(RedirectSource)
		if !ok {
			continue
		}
		var rules []BuiltRedirect
		if err := runHook(p.Name(), "Redirects", func() error {
			rules = source.Redirects()
			return nil
		}); err != nil {
			return nil, err
		}
		for _, rule := range rules {
			if err := types.RedirectTextError(rule.From, rule.To); err != nil {
				return nil, fmt.Errorf("collage: plugin %q redirects: %w", p.Name(), err)
			}
			rule.Source = p.Name()
			out = append(out, rule)
		}
	}
	return out, nil
}

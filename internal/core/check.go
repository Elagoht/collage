package core

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

// The rules Check reports under.
const (
	// RuleUnknownRoute is a link to a page, document, action or fragment path
	// that is not registered.
	RuleUnknownRoute = "unknown-route"
	// RuleRouteParams is a link whose parameters do not fill its route's pattern.
	RuleRouteParams = "route-params"
	// RuleUnreachableLocale is a link into a locale no URL can carry.
	RuleUnreachableLocale = "unreachable-locale"
	// RuleNoPathInLocale is a link into a locale the route has no path in.
	RuleNoPathInLocale = "no-path-in-locale"
)

// urlFuncs are the template functions that build a link by name, with how many
// arguments come before their parameter pairs.
var urlFuncs = map[string]int{
	"pageURL":       1,
	"pageURLIn":     2,
	"actionURL":     1,
	"fragmentURL":   2,
	"fragmentURLIn": 3,
	"localeURL":     1,
}

// Check reports what is wrong with the application that can be told without
// rendering it, as findings: a template linking by name — {{pageURL "post"}},
// {{actionURL "logout"}}, {{fragmentURL "home" "clock"}} — to something not
// registered, into a locale no URL reaches or the route has no path in, or with
// parameters its pattern does not take. Each of those is an error when the
// template renders, on the one page that reaches it; Check finds them all at
// once, in every template, before anything renders.
//
// It reads only what is written as a string literal. A name given by a field or
// a variable is not known until the template renders, and is not checked; a
// parameter value never is, since it only fills a pattern. A link with no locale
// of its own is checked the way it renders: it is fine when the route can be
// built in any locale, as a render falls back to the default one.
//
// A started application is checked as it will serve; Check before Start sees
// what has been registered so far.
func (a *App) Check() []types.Finding {
	defaultLocale, locales := a.Locales()
	ordered := append([]string{defaultLocale}, slices.DeleteFunc(slices.Clone(locales), func(l string) bool { return l == defaultLocale })...)

	var findings []types.Finding
	for _, call := range a.tmpl.Calls(slices.Sorted(maps.Keys(urlFuncs))...) {
		if f, ok := a.checkCall(call, ordered); ok {
			findings = append(findings, f)
		}
	}
	return findings
}

// checkCall returns the finding about one link, if there is one.
func (a *App) checkCall(call template.Call, locales []string) (types.Finding, bool) {
	fixed := urlFuncs[call.Func]
	finding := func(rule, message string) (types.Finding, bool) {
		return types.Finding{
			Level:   types.FindingError,
			Rule:    rule,
			Message: types.HumanizeTemplateNames(call.Location) + ": " + describeCall(call) + ": " + message,
		}, true
	}

	if len(call.Args) < fixed {
		return finding(RuleRouteParams, fmt.Sprintf("%s takes %d arguments before its parameters, and has %d", call.Func, fixed, len(call.Args)))
	}
	for _, arg := range call.Args[:fixed] {
		if !arg.Literal {
			return types.Finding{}, false
		}
	}
	names := make([]string, fixed)
	for i, arg := range call.Args[:fixed] {
		names[i] = arg.Text
	}

	pairs := call.Args[fixed:]
	if len(pairs)%2 != 0 {
		return finding(RuleRouteParams, fmt.Sprintf("its parameters are not name and value pairs: %d arguments after the name", len(pairs)))
	}
	params := make(map[string]string, len(pairs)/2)
	paramsKnown := true
	for i := 0; i < len(pairs); i += 2 {
		if !pairs[i].Literal {
			paramsKnown = false
			continue
		}
		// A placeholder: only whether each parameter is there matters here.
		params[pairs[i].Text] = "x"
	}

	if call.Func == "localeURL" {
		if !a.localeReachable(names[0]) {
			return finding(RuleUnreachableLocale, fmt.Sprintf("no URL can carry the locale %q", names[0]))
		}
		return types.Finding{}, false
	}

	var try []string
	var build func(locale string) error
	switch call.Func {
	case "pageURL":
		try = locales
		build = func(l string) error { _, err := a.URL(names[0], l, params); return err }
	case "pageURLIn":
		try = []string{names[0]}
		build = func(l string) error { _, err := a.URL(names[1], l, params); return err }
	case "actionURL":
		try = locales
		build = func(l string) error { _, err := a.ActionURL(names[0], l, params); return err }
	case "fragmentURL":
		try = locales
		build = func(l string) error { _, err := a.FragmentURL(names[0], names[1], l, params); return err }
	case "fragmentURLIn":
		try = []string{names[0]}
		build = func(l string) error { _, err := a.FragmentURL(names[1], names[2], l, params); return err }
	}

	var failure error
	for _, locale := range try {
		err := build(locale)
		if err == nil {
			return types.Finding{}, false
		}
		// The first failure that is not merely a missing translation says the
		// most; a missing path counts only when no locale has one.
		if failure == nil || (errors.Is(failure, types.ErrNoPathInLocale) && !errors.Is(err, types.ErrNoPathInLocale)) {
			failure = err
		}
	}

	switch {
	case errors.Is(failure, ErrLocaleUnreachable):
		return finding(RuleUnreachableLocale, failure.Error())
	case errors.Is(failure, types.ErrNoPathInLocale):
		return finding(RuleNoPathInLocale, failure.Error())
	case errors.Is(failure, types.ErrRouteParams):
		if !paramsKnown {
			return types.Finding{}, false
		}
		return finding(RuleRouteParams, failure.Error())
	case errors.Is(failure, types.ErrUnknownRoute), errors.Is(failure, types.ErrUnknownFragmentPath):
		return finding(RuleUnknownRoute, failure.Error()+a.suggest(call.Func, names))
	default:
		return finding(RuleUnknownRoute, failure.Error())
	}
}

// describeCall writes call back as the template did, its unknown arguments as
// dots: {{pageURL "post" "slug" .}}.
func describeCall(call template.Call) string {
	parts := []string{call.Func}
	for _, arg := range call.Args {
		if arg.Literal {
			parts = append(parts, strconv.Quote(arg.Text))
		} else {
			parts = append(parts, ".")
		}
	}
	return "{{" + strings.Join(parts, " ") + "}}"
}

// suggest returns "; did you mean ..." naming the registered name closest to
// the unknown one a link used, or "" when none is close.
func (a *App) suggest(fn string, names []string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var unknown string
	var candidates []string
	switch fn {
	case "pageURL", "pageURLIn":
		unknown = names[len(names)-1]
		candidates = append(slices.Collect(maps.Keys(a.pages)), slices.Collect(maps.Keys(a.documents))...)
	case "actionURL":
		unknown = names[0]
		candidates = slices.Collect(maps.Keys(a.actions))
	case "fragmentURL", "fragmentURLIn":
		page, fragment := names[len(names)-2], names[len(names)-1]
		p := a.pages[page]
		if p == nil {
			unknown, candidates = page, slices.Collect(maps.Keys(a.pages))
			break
		}
		unknown = fragment
		for _, byPattern := range p.FragmentPaths {
			for _, f := range byPattern {
				if f != nil && !slices.Contains(candidates, f.Name) {
					candidates = append(candidates, f.Name)
				}
			}
		}
	}

	best, bestDistance := "", len(unknown)/3+2
	slices.Sort(candidates)
	for _, candidate := range candidates {
		if d := editDistance(unknown, candidate); d < bestDistance {
			best, bestDistance = candidate, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf("; did you mean %q?", best)
}

// editDistance is the Levenshtein distance between a and b, in bytes.
func editDistance(a, b string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}

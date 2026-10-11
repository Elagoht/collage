package cli

import (
	"fmt"
	"strings"

	"github.com/Elagoht/collage/internal/ascii"
	"github.com/Elagoht/collage/internal/cli/plugincontract"
)

// bundle is a named set of plugins that "plugin add", "plugin del" and
// "new --with" take as @name. Its plugins are added in the order listed; each
// one's place in plugins.go is still its manifest's order.
type bundle struct {
	Name    string   // without the @
	Short   string   // what it is for, in a few words
	Plugins []string // owner/name references
}

// bundles are the bundles collage knows, in the order the usage lists them. A
// variable so a test can put fixtures in their place.
var bundles = []bundle{
	{"prod", "what a site in production wants", []string{
		"elagoht/health", "elagoht/secure", "elagoht/compress", "elagoht/minimizer",
		"elagoht/accesslog", "elagoht/ratelimit",
	}},
	{"site", "a public site's search and share basics", []string{
		"elagoht/robots", "elagoht/sitemap", "elagoht/meta", "elagoht/jsonld", "elagoht/favicon",
	}},
	{"blog", "@site, and posts written in Markdown", []string{
		"elagoht/robots", "elagoht/sitemap", "elagoht/meta", "elagoht/jsonld", "elagoht/favicon",
		"elagoht/markdown", "elagoht/feed", "elagoht/toc", "elagoht/highlight", "elagoht/ogimage",
	}},
	{"forms", "forms, and state kept in cookies", []string{
		"elagoht/session", "elagoht/flash", "elagoht/validate", "elagoht/honeypot",
	}},
	{"live", "fragments the server keeps current", []string{
		"elagoht/live", "elagoht/websocket",
	}},
	{"ops", "metrics and error reports", []string{
		"elagoht/prometheus", "elagoht/errortrack",
	}},
}

// findBundle returns the bundle named name, without its @.
func findBundle(name string) (bundle, bool) {
	for _, b := range bundles {
		if b.Name == name {
			return b, true
		}
	}
	return bundle{}, false
}

// bundleNames is every bundle's name with its @, for an error message.
func bundleNames() string {
	var names []string
	for _, b := range bundles {
		names = append(names, "@"+b.Name)
	}
	return strings.Join(names, ", ")
}

// namedRef is one plugin a command was given, directly or through a bundle.
type namedRef struct {
	ref   plugincontract.Ref
	label string // how it is named to the user: owner/name, or the module path
	// fromBundle is whether only a bundle named it; del passes over such a
	// plugin when it is not there.
	fromBundle bool
}

// expandRefs reads a command's plugin arguments: owner/name, a module path,
// either with an @version, or a bundle as @name, which stands for its plugins.
// A plugin named twice is kept once, at its first place; named with two
// versions it is an error, as is anything that cannot be read, so a command
// refuses before it changes anything.
func expandRefs(args []string) ([]namedRef, error) {
	var refs []namedRef
	for _, arg := range args {
		if name, ok := strings.CutPrefix(arg, "@"); ok {
			b, found := findBundle(name)
			if !found {
				return nil, fmt.Errorf("%q is not a bundle; the bundles are %s", arg, bundleNames())
			}
			for _, p := range b.Plugins {
				ref, err := plugincontract.ParseRef(p)
				if err != nil {
					return nil, fmt.Errorf("@%s: %w", b.Name, err)
				}
				if refs, err = addRef(refs, namedRef{ref: ref, label: ref.Short, fromBundle: true}); err != nil {
					return nil, err
				}
			}
			continue
		}
		ref, err := plugincontract.ParseRef(arg)
		if err != nil {
			if _, found := findBundle(arg); found {
				return nil, fmt.Errorf("%w; a bundle is written @%s", err, arg)
			}
			return nil, err
		}
		label := ref.Short
		if label == "" {
			label = ref.Path
		}
		if refs, err = addRef(refs, namedRef{ref: ref, label: label}); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

// addRef appends r to refs, or merges it into the entry for the same module.
func addRef(refs []namedRef, r namedRef) ([]namedRef, error) {
	for i, have := range refs {
		if !ascii.EqualFold(have.ref.Path, r.ref.Path) {
			continue
		}
		if have.ref.Version != "" && r.ref.Version != "" && have.ref.Version != r.ref.Version {
			return nil, fmt.Errorf("%s is given with two versions, %s and %s", have.label, have.ref.Version, r.ref.Version)
		}
		if have.ref.Version == "" {
			refs[i].ref.Version = r.ref.Version
		}
		refs[i].fromBundle = have.fromBundle && r.fromBundle
		return refs, nil
	}
	return append(refs, r), nil
}

// bundleUsage lists the bundles for the usage text: each one's name and what
// it is for, then its plugins, wrapped under it.
func bundleUsage() string {
	var b strings.Builder
	for _, bd := range bundles {
		fmt.Fprintf(&b, "  @%s  %s\n", bd.Name, bd.Short)
		line := "     "
		for i, p := range bd.Plugins {
			word := " " + p
			if i < len(bd.Plugins)-1 {
				word += ","
			}
			if len(line)+len(word) > 80 {
				b.WriteString(line + "\n")
				line = "     "
			}
			line += word
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

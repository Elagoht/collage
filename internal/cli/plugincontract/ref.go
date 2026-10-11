// Package plugincontract is the rule a module meets to be a collage plugin
// "collage plugin add" accepts: a manifest at its root, a well-formed name, a
// New it can call without configuration, a release tag. It reads a module on
// disk and says which rule it breaks.
package plugincontract

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// AuthorsURL is the page that states the contract; every refusal points at it.
const AuthorsURL = "https://collage.furkanbaytekin.dev/en/docs/plugin-authors/"

// Ref is a plugin reference as typed after "collage plugin add" or "del".
type Ref struct {
	// Path is the module path to fetch.
	Path string
	// Short is "owner/name", lowercased, when the reference was short; "" for a
	// full module path.
	Short string
	// Version is what followed "@", or "" when nothing did.
	Version string
}

var segment = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ParseRef reads s: "owner/name" (resolved by ConventionalPath) or a module
// path whose first segment has a dot, either one optionally followed by
// "@version".
func ParseRef(s string) (Ref, error) {
	path, version, hasAt := strings.Cut(s, "@")
	if hasAt && version == "" {
		return Ref{}, fmt.Errorf("%q: an empty version after @", s)
	}
	parts := strings.Split(path, "/")
	if len(parts) > 0 && strings.Contains(parts[0], ".") {
		for _, p := range parts {
			if p == "" || strings.ContainsAny(p, " \t\\") {
				return Ref{}, fmt.Errorf("%q is not a module path", path)
			}
		}
		if len(parts) < 2 {
			return Ref{}, fmt.Errorf("%q is not a module path", path)
		}
		return Ref{Path: path, Version: version}, nil
	}
	if len(parts) != 2 {
		return Ref{}, fmt.Errorf("%q: want owner/name, or a module path such as github.com/owner/repo", s)
	}
	short := strings.ToLower(path)
	owner, name, _ := strings.Cut(short, "/")
	if !segment.MatchString(owner) || !segment.MatchString(name) {
		return Ref{}, fmt.Errorf("%q: owner and name are letters, digits and dashes", s)
	}
	return Ref{Path: ConventionalPath(short), Short: short, Version: version}, nil
}

// ConventionalPath is where a short name's module lives:
// "elagoht/consent" is github.com/elagoht/collage-consent.
func ConventionalPath(short string) string {
	owner, name, _ := strings.Cut(short, "/")
	return "github.com/" + owner + "/collage-" + name
}

var pseudo = regexp.MustCompile(`(^|[-.])\d{14}-[0-9a-f]{12}(\+incompatible)?$`)

// IsPseudoVersion reports whether v is a pseudo-version: a commit, not a
// release.
func IsPseudoVersion(v string) bool { return pseudo.MatchString(v) }

// errContract wraps every rule refusal, so a caller can tell a refusal from an
// I/O failure.
var errContract = errors.New("not a collage plugin")

package types

import "time"

// FindingLevel is how serious a Finding is.
type FindingLevel int

const (
	// FindingWarning is worth fixing and does not stop anything: it is shown in
	// development and listed in a build's report.
	FindingWarning FindingLevel = iota + 1
	// FindingError is a mistake: it is shown in development, and it fails a
	// static build.
	FindingError
)

// String names the level as a report prints it.
func (l FindingLevel) String() string {
	switch l {
	case FindingWarning:
		return "warning"
	case FindingError:
		return "error"
	default:
		return "finding"
	}
}

// Finding is something a plugin checking the output noticed about a page: a
// heading level skipped, an image without alt text, a link to nothing.
//
// It is not a failure. The page renders and is served as it is; a finding is
// shown over the page in development and listed in a build's report, and an
// error-level one fails the build — which is where a check belongs, since a
// production server re-checking every page it renders would spend its time on
// what the build already knew.
type Finding struct {
	// Level is how serious it is.
	Level FindingLevel
	// Plugin is the plugin that reported it. The framework fills it in.
	Plugin string
	// Rule names the check, so it can be looked up and turned off: "heading-order",
	// "img-alt".
	Rule string
	// Message says what is wrong, where, in a sentence.
	Message string
	// Path is the URL of the page it is about. The framework fills it in for a
	// finding about one render; a check across the whole build sets it itself.
	Path string
}

// PathTagPrefix begins the dependency tag every cached entry carries for the URL
// path it was rendered for. See PathTag.
const PathTagPrefix = "collage:path:"

// PathTag is the dependency tag of the cached entries rendered for path:
// invalidating it drops them, whatever else they depend on.
func PathTag(path string) string { return PathTagPrefix + path }

// FragmentReport is how one fragment of a render went: what a development tool
// shows beside the page.
type FragmentReport struct {
	// Name is the fragment's name.
	Name string
	// Duration is its wall-clock time, the fragments in its slots included.
	Duration time.Duration
	// Failed reports that its own render failed, even when a fallback stood in.
	Failed bool
	// UsedFallback reports that its fallback rendered in its place.
	UsedFallback bool
	// Err is what it failed with, or nil.
	Err error
}

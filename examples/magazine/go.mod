// The Wire — a magazine built with collage.
//
// An application that uses the framework: its own module, its own module path, and
// collage as an ordinary dependency resolved from the module cache like any other.
//
// The three plugin requires carry replace directives and the collage one does not,
// which is the difference between a dependency that is published and one that is
// not. collage has a version; the plugins live beside this example under
// placeholder module paths, so there is nowhere for the module cache to fetch them
// from. A real third-party plugin — one with a real module path and a tag — needs
// neither the replace nor a mention here beyond the require.
module example.com/thewire

go 1.26

require (
	example.com/jsonld v0.0.0
	example.com/minimizer v0.0.0
	example.com/opti-image v0.0.0
	github.com/Elagoht/collage v0.1.0
)

replace example.com/jsonld => ../../plugins/jsonld

replace example.com/minimizer => ../../plugins/minimizer

replace example.com/opti-image => ../../plugins/opti-image

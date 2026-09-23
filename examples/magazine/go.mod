// The Wire — a magazine built with collage.
//
// An application that uses the framework: its own module, its own module path, and
// collage as an ordinary dependency resolved from the module cache like any other.
// Nothing in this directory is framework source, and nothing here can reach the
// framework's internal packages — Go does not let one module import another's.
//
// The go.work at the repository root points this at the checkout beside it while
// the two are developed together. It is a developer's tool and nothing in this
// module refers to it — delete it, or copy this directory elsewhere, and the
// require below resolves from the module cache as usual.
module example.com/thewire

go 1.26

require (
	github.com/Elagoht/collage v0.2.0
	github.com/Elagoht/collage-jsonld v0.0.0-20260923121014-0725f00da048
	github.com/Elagoht/collage-minimizer v0.0.0-20260923121011-ca8447f9380d
	github.com/Elagoht/collage-opti-image v0.0.0-20260923125628-ba6632d57ea6
)

require (
	github.com/HugoSmits86/nativewebp v1.3.0 // indirect
	golang.org/x/image v0.24.0 // indirect
)

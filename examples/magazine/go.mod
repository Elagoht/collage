// The Wire — a magazine built with collage.
//
// This is what an application using the framework looks like from the outside: its
// own module, its own module path, and collage as an ordinary dependency. Nothing
// in the site's code is special because it happens to live in the framework's
// repository.
//
// The replace below is the one line that is. It points the dependency at the
// checkout two directories up, so the example is built against the collage you have
// rather than a published version — the framework has no released tag yet, and an
// example that lags the code it demonstrates is worse than no example. Your own
// project would not have it: you would `go get github.com/Elagoht/collage` and
// require a version.
module example.com/thewire

go 1.26

require github.com/Elagoht/collage v0.0.0

replace github.com/Elagoht/collage => ../..

// The Wire — a magazine built with collage.
//
// An application that uses the framework: its own module, its own module path, and
// collage as an ordinary dependency resolved from the module cache like any other.
// Nothing in this directory is framework source, and nothing here can reach the
// framework's internal packages — Go does not allow it across modules.
//
// The go.work file at the repository root points this at the checkout beside it
// while the two are developed together. It is a developer's tool, not part of the
// project: delete it and this module builds against the released version instead,
// which is what anyone cloning the example on its own gets.
module example.com/thewire

go 1.26

require github.com/Elagoht/collage v0.1.0

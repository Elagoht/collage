// A collage plugin that strips whitespace and comments from what the application
// serves. It is a separate module because a plugin is a separate project: it
// requires collage the way any consumer would, and nothing about living in the
// framework's repository gives it access the framework does not give everyone.
module example.com/minimizer

go 1.26

require github.com/Elagoht/collage v0.1.0

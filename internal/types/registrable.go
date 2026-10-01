package types

// Registrable is what App.Register takes: a *Page, a *Document or an *Action. The
// unexported method seals it, so nothing outside collage can claim to be one and
// reach Register with a kind it has no way to register.
type Registrable interface {
	registrable()
}

func (*Page) registrable()     {}
func (*Document) registrable() {}
func (*Action) registrable()   {}

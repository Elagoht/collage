package collage

import "github.com/Elagoht/collage/internal/plugin"

// FindPlugin returns the application's plugin of type T, and whether there is
// one: the first in the list, when several are. host is the ConfigHost given to
// Configure or the Host given to Init. The plugin is found wherever it is
// listed, and may not be configured or initialised yet. A plugin that needs
// another to be set up first calls it from Configure, which runs before any
// plugin is built.
//
//	lv, ok := collage.FindPlugin[*live.Plugin](host)
//
// A host collage did not make (a test double) has no plugins, and FindPlugin
// reports false.
func FindPlugin[T Plugin](host ConfigReader) (T, bool) {
	var zero T
	lister, ok := host.(plugin.Lister)
	if !ok {
		return zero, false
	}
	for _, p := range lister.ApplicationPlugins() {
		if t, ok := p.(T); ok {
			return t, true
		}
	}
	return zero, false
}

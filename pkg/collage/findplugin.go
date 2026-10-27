package collage

import "github.com/Elagoht/collage/internal/plugin"

// FindPlugin returns the application's plugin of type T, and whether there is
// one: the first in the list, when several are. host is the ConfigHost given to
// Configure; the plugin is found wherever it is listed, and may not be
// configured yet. Call it from Configure, which runs before any plugin is
// built, and keep what it finds: the Host given to Init lists no plugins, so
// FindPlugin from Init reports false.
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

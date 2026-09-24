package collage

import (
	"github.com/Elagoht/collage/internal/core"
	"github.com/Elagoht/collage/internal/types"
)

// ErrUnknownRoute is returned by App.URL and the URL template functions for a
// name no page or document was registered under.
var ErrUnknownRoute = types.ErrUnknownRoute

// ErrNoPathInLocale is returned by App.URL and {{pageURLIn}} for a route with no
// path in the locale asked for.
var ErrNoPathInLocale = types.ErrNoPathInLocale

// ErrRouteParams is returned when the parameters given for a URL do not fill the
// route's pattern exactly.
var ErrRouteParams = types.ErrRouteParams

// ErrLocaleUnreachable is returned by App.URL for a locale no URL can carry.
var ErrLocaleUnreachable = core.ErrLocaleUnreachable

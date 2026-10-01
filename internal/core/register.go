package core

import (
	"errors"
	"fmt"

	"github.com/Elagoht/collage/internal/types"
)

// ErrNilRegistrable reports a nil passed to Register, where a page, a document or
// an action was expected.
var ErrNilRegistrable = errors.New("collage: nil passed to Register")

// Register registers each of items in order — a page through RegisterPage, a
// document through RegisterDocument, an action through RegisterAction — and stops
// at the first one refused, returning its error wrapped with its kind and name:
//
//	if err := app.Register(
//		pages.Home(),
//		pages.About(),
//		documents.Health(),
//		actions.Logout(),
//	); err != nil {
//		return err // "register page "about": collage: duplicate page ..."
//	}
//
// It is the loop a project's routes.go would otherwise write once per kind. Each
// item goes through exactly the checks its own Register method makes; what was
// registered before a refusal stays registered, as it does for those methods, so
// a refusal is a startup failure to abort on.
//
// The not-found and error pages are not among them: they are reached by failing
// to match rather than by a path, and RegisterNotFoundPage and RegisterErrorPage
// say which is which.
func (a *App) Register(items ...types.Registrable) error {
	for i, item := range items {
		var kind, name string
		var err error
		switch item := item.(type) {
		case *types.Page:
			kind = "page"
			if item != nil {
				name = item.Name
			}
			err = a.RegisterPage(item)
		case *types.Document:
			kind = "document"
			if item != nil {
				name = item.Name
			}
			err = a.RegisterDocument(item)
		case *types.Action:
			kind = "action"
			if item != nil {
				name = item.Name
			}
			err = a.RegisterAction(item)
		default:
			return fmt.Errorf("%w: item %d", ErrNilRegistrable, i)
		}
		if err != nil {
			return fmt.Errorf("register %s %q: %w", kind, name, err)
		}
	}
	return nil
}

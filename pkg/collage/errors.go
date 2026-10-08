package collage

import (
	"github.com/Elagoht/collage/internal/core"
	"github.com/Elagoht/collage/internal/csrf"
	"github.com/Elagoht/collage/internal/httpx"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/template"
)

// Registering several things at once.
var (
	// ErrNilRegistrable is returned by Register for a nil item, where a page, a
	// document or an action was expected.
	ErrNilRegistrable = core.ErrNilRegistrable
)

// Registering an action.
var (
	// ErrNilAction is returned by RegisterAction for a nil action.
	ErrNilAction = core.ErrNilAction
	// ErrDuplicateAction is returned for a second action registered under a name
	// already taken.
	ErrDuplicateAction = core.ErrDuplicateAction
	// ErrEmptyActionName is returned for an action with no name.
	ErrEmptyActionName = core.ErrEmptyActionName
	// ErrNoActionPaths is returned for a standalone action with no path.
	ErrNoActionPaths = core.ErrNoActionPaths
	// ErrNoActionHandler is returned for an action with no handler.
	ErrNoActionHandler = core.ErrNoActionHandler
	// ErrNoMethods is returned for an action that answers no method.
	ErrNoMethods = router.ErrNoMethods
	// ErrInvalidActionMethod is returned for an action that declares OPTIONS,
	// TRACE or CONNECT. The router answers OPTIONS itself, and an action
	// claiming it would skip the forgery check.
	ErrInvalidActionMethod = router.ErrInvalidActionMethod
	// ErrNilFragmentPath is returned for a WithFragmentPath given no fragment.
	ErrNilFragmentPath = core.ErrNilFragmentPath
)

// Request-forgery refusals, reported to error hooks when a submission is refused
// with a 403.
var (
	// ErrCSRFMissing reports a submission that carried no token, or no cookie.
	ErrCSRFMissing = csrf.ErrMissing
	// ErrCSRFMismatch reports a token that is not the one in the cookie.
	ErrCSRFMismatch = csrf.ErrMismatch
	// ErrCSRFInvalid reports a token this application did not sign.
	ErrCSRFInvalid = csrf.ErrInvalid
	// ErrCSRFCrossOrigin reports a submission the browser marked as sent from
	// another origin — by Sec-Fetch-Site, or by an Origin that is not the Host —
	// refused whatever token it carried. An origin whose forms may post here is
	// named in Security.CSRFTrustedOrigins.
	ErrCSRFCrossOrigin = csrf.ErrCrossOrigin
	// ErrCSRFDisabled is returned by {{csrfToken}} when forgery protection is off.
	ErrCSRFDisabled = core.ErrCSRFDisabled
)

// ErrMethodNotAllowed is reported for a request whose path exists but answers no
// such method: a 405, with an Allow header naming what it does answer.
var ErrMethodNotAllowed = httpx.ErrMethodNotAllowed

// ErrNoMountForAsset reports an asset path under a prefix no mount serves; it is
// wrapped in ErrUnknownAsset.
var ErrNoMountForAsset = core.ErrNoMountForAsset

// ErrTemplateEscapesRoot is returned by New for a template path that resolves
// outside Template.Root.
var ErrTemplateEscapesRoot = template.ErrTemplateEscapesRoot

// ErrSourceConflict is returned at registration for an inline template that
// defines a template of its own, which would replace a file template of that name.
var ErrSourceConflict = template.ErrSourceConflict

// Mistakes in a {{dict}} call.
var (
	// ErrDictOddArgs reports {{dict}} given a key with no value.
	ErrDictOddArgs = template.ErrDictOddArgs
	// ErrDictKeyNotString reports a {{dict}} key that is not a string.
	ErrDictKeyNotString = template.ErrDictKeyNotString
)

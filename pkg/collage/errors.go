package collage

import (
	"github.com/Elagoht/collage/internal/core"
	"github.com/Elagoht/collage/internal/csrf"
	"github.com/Elagoht/collage/internal/httpx"
	"github.com/Elagoht/collage/internal/router"
	"github.com/Elagoht/collage/internal/template"
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

// Mistakes in a {{dict}} call.
var (
	// ErrDictOddArgs reports {{dict}} given a key with no value.
	ErrDictOddArgs = template.ErrDictOddArgs
	// ErrDictKeyNotString reports a {{dict}} key that is not a string.
	ErrDictKeyNotString = template.ErrDictKeyNotString
)

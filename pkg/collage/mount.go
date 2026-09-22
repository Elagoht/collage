package collage

import (
	"github.com/Elagoht/collage/internal/asset"
	"github.com/Elagoht/collage/internal/core"
)

// Mount is a mounted asset file system served under a URL prefix, as returned by
// App.Mounts. It is aliased here, not merely returned, because App.Mounts's
// element type is otherwise unnameable outside this module: internal/asset,
// where it actually lives, is unreachable from an external caller, who could
// still call Mounts but could not declare a variable, a slice, or a function
// parameter of its element type without this alias.
type Mount = asset.Mount

// MountOption configures a mounted asset file system. See WithCacheControl and
// WithoutBuildCopy.
type MountOption = asset.Option

// WithCacheControl sets the Cache-Control header served with every file in a
// mount.
var WithCacheControl = asset.WithCacheControl

// WithoutBuildCopy stops a static build from copying a mount into its output. Use
// it for a mount served from a CDN in production, or one large enough that
// duplicating it into the build directory is not wanted.
var WithoutBuildCopy = asset.WithoutBuildCopy

// ErrInvalidPrefix reports a mount prefix that is empty, "/", or not a
// slash-delimited path. A mount at "/" would swallow every route.
var ErrInvalidPrefix = asset.ErrInvalidPrefix

// ErrNilFS reports that a mount was given no file system.
var ErrNilFS = asset.ErrNilFS

// ErrMountShadowsRoute reports that a mount prefix would swallow a registered page
// or document path, making that route unreachable.
var ErrMountShadowsRoute = core.ErrMountShadowsRoute

// ErrMountConflict reports that two mounts claim overlapping prefixes.
var ErrMountConflict = core.ErrMountConflict

package collage

import (
	"github.com/Elagoht/collage/internal/asset"
)

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

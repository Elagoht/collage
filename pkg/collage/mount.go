package collage

import (
	"github.com/Elagoht/collage/internal/asset"
)

// MountOption configures a mounted asset file system. See WithCacheControl and
// WithoutBuildCopy.
type MountOption = asset.Option

// WithCacheControl sets the Cache-Control header served with every file in a
// mount.
func WithCacheControl(value string) MountOption {
	return asset.WithCacheControl(value)
}

// WithoutBuildCopy stops a static build from copying a mount into its output. Use
// it for a mount served from a CDN in production, or one large enough that
// duplicating it into the build directory is not wanted.
func WithoutBuildCopy() MountOption {
	return asset.WithoutBuildCopy()
}

// WithAttachments serves html, xhtml, svg and xml files in a mount with
// "Content-Disposition: attachment", so a browser downloads them instead of
// running their script on the site's origin. Use it for a mount of uploaded files;
// without it a stored "up.html" is a page of the reader's own making at your
// address. Content with no extension is already never served as markup, and other
// types are unaffected.
func WithAttachments() MountOption {
	return asset.WithAttachments()
}

package optiimage

import (
	"errors"
	"image"
	"io"
)

// errNoWebPEncoder is returned when WebP output is asked for and nothing can
// produce it.
var errNoWebPEncoder = errors.New("opti-image: no WebP encoder is linked")

// WebPEncoder encodes an image as WebP.
//
// It is an interface the application supplies rather than something this package
// implements, because the standard library has no WebP encoder and every working
// one needs cgo. A plugin that cannot be built without a C toolchain is a plugin
// most people cannot use, so the default build has none and re-encodes to the
// source format instead.
//
// To get WebP, build with -tags webp and register an encoder:
//
//	//go:build webp
//	func init() { optiimage.RegisterWebPEncoder(myCgoEncoder{}) }
//
// The build tag is what makes the dependency opt-in; RegisterWebPEncoder is what
// makes it replaceable. Both are needed: the tag alone would still require this
// package to import something, and the registration alone would leave a binary
// carrying a cgo dependency nobody asked for.
type WebPEncoder interface {
	// EncodeWebP writes img to w at the given quality, 1..100.
	EncodeWebP(w io.Writer, img image.Image, quality int) error
}

// webpEncoder is what RegisterWebPEncoder set, or nil.
var webpEncoder WebPEncoder

// RegisterWebPEncoder installs the encoder used when Config.WebP is on. It is
// intended for an init function in a build-tagged file, and is not safe to call
// once an application is serving.
func RegisterWebPEncoder(enc WebPEncoder) { webpEncoder = enc }

// encodeWebP encodes img, or reports that nothing can.
func encodeWebP(w io.Writer, img image.Image, quality int) error {
	if webpEncoder == nil {
		return errNoWebPEncoder
	}
	return webpEncoder.EncodeWebP(w, img, quality)
}

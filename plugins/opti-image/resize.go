package optiimage

import (
	"image"
	"image/color"
)

// resize scales src to width by height using area averaging.
//
// Area averaging — each destination pixel is the mean of the source pixels it
// covers — rather than nearest neighbour, because this plugin exists to downscale
// and nearest neighbour downscaling is where aliasing comes from: a one-pixel line
// in the source either survives whole or vanishes, so a photograph of a building
// acquires moiré and a screenshot of text becomes unreadable.
//
// It is the standard library only. x/image/draw's CatmullRom would be sharper on
// upscales, and a plugin that drags a dependency in for a case it is not built for
// is a plugin with a dependency. Upscaling here is a box filter, which is soft; an
// application upscaling images has asked for the wrong thing anyway, since the
// pixels it wants do not exist.
func resize(src image.Image, width, height int) image.Image {
	bounds := src.Bounds()
	if width <= 0 || height <= 0 || bounds.Empty() {
		return src
	}
	if bounds.Dx() == width && bounds.Dy() == height {
		return src
	}

	dst := image.NewNRGBA(image.Rect(0, 0, width, height))

	scaleX := float64(bounds.Dx()) / float64(width)
	scaleY := float64(bounds.Dy()) / float64(height)

	for y := range height {
		y0 := bounds.Min.Y + int(float64(y)*scaleY)
		y1 := bounds.Min.Y + int(float64(y+1)*scaleY)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := range width {
			x0 := bounds.Min.X + int(float64(x)*scaleX)
			x1 := bounds.Min.X + int(float64(x+1)*scaleX)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			dst.SetNRGBA(x, y, average(src, x0, y0, x1, y1))
		}
	}
	return dst
}

// average is the mean colour of the source rectangle, computed on premultiplied
// values.
//
// Premultiplied, because averaging straight alpha blends a transparent pixel's
// colour into its neighbours: a fully transparent black pixel beside an opaque
// white one averages to translucent grey rather than translucent white, and a
// downscaled logo acquires a dark halo. Go's image.Image gives premultiplied values
// from RGBA(), so the arithmetic is done there and converted back at the end.
func average(src image.Image, x0, y0, x1, y1 int) color.NRGBA {
	var sumR, sumG, sumB, sumA uint64
	count := uint64((x1 - x0) * (y1 - y0))
	if count == 0 {
		return color.NRGBA{}
	}

	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			r, g, b, a := src.At(x, y).RGBA()
			sumR += uint64(r)
			sumG += uint64(g)
			sumB += uint64(b)
			sumA += uint64(a)
		}
	}

	a := sumA / count
	if a == 0 {
		return color.NRGBA{}
	}
	// Back to straight alpha: divide the premultiplied channels by the alpha.
	unpremultiply := func(sum uint64) uint8 {
		v := sum / count * 0xffff / a
		if v > 0xffff {
			v = 0xffff
		}
		return uint8(v >> 8)
	}
	return color.NRGBA{
		R: unpremultiply(sumR),
		G: unpremultiply(sumG),
		B: unpremultiply(sumB),
		A: uint8(a >> 8),
	}
}

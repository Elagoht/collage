package main

import (
	"crypto/sha256"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
)

// Article images are generated rather than stored.
//
// A corpus carrying real photographs would make this example a download, and
// checking a few megabytes of JPEG into a repository to demonstrate a resize is a
// poor trade. What matters for the demonstration is that the endpoint serves a real
// image, at a real size, deterministically — so a client can be resized against it
// and the result compared.

// imageWidth and imageHeight are what the endpoint serves. Deliberately larger than
// anything a page displays, because the point of a client-side optimiser is to be
// handed something too big.
const (
	imageWidth  = 1600
	imageHeight = 900
)

// serveImage writes a generated image for a slug.
//
// The colours come from a hash of the slug, so one article's image is the same on
// every request and different from its neighbour's — which is what makes a resized
// copy checkable by eye as well as by size.
func (s *server) serveImage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSuffix(r.PathValue("slug"), ".png")
	if _, err := s.store.Article(slug); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	w.Header().Set("Content-Type", "image/png")
	// A generated image never changes, so it is worth saying so: a client that
	// re-fetches it on every page view is measuring this endpoint rather than the
	// optimiser in front of it.
	w.Header().Set("Cache-Control", "public, max-age=86400")
	png.Encode(w, generateImage(slug))
}

// generateImage draws a two-tone diagonal gradient keyed to slug.
func generateImage(slug string) image.Image {
	sum := sha256.Sum256([]byte(slug))
	from := color.NRGBA{R: sum[0], G: sum[1], B: sum[2], A: 255}
	to := color.NRGBA{R: sum[3], G: sum[4], B: sum[5], A: 255}

	img := image.NewNRGBA(image.Rect(0, 0, imageWidth, imageHeight))
	for y := range imageHeight {
		for x := range imageWidth {
			// Diagonal, so a resize that got the aspect ratio wrong is visible as a
			// change in the gradient's angle rather than only in the dimensions.
			t := float64(x+y) / float64(imageWidth+imageHeight)
			img.SetNRGBA(x, y, color.NRGBA{
				R: lerp(from.R, to.R, t),
				G: lerp(from.G, to.G, t),
				B: lerp(from.B, to.B, t),
				A: 255,
			})
		}
	}
	return img
}

func lerp(a, b uint8, t float64) uint8 {
	return uint8(float64(a) + (float64(b)-float64(a))*t)
}

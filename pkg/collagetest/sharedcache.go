package collagetest

import (
	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/pkg/collage"
)

// NewSharedCache returns an in-memory collage.SharedCache for tests of an
// application that runs as a fleet. It is safe for concurrent use. Two Apps given
// the same value behave as two instances on one store, the way two processes do
// on one Redis: a page one renders is served by the other, and an invalidation
// one sends is seen by both.
func NewSharedCache() collage.SharedCache {
	return cache.NewSharedMemory()
}

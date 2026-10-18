package cache_test

import (
	"testing"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

func TestSuite_Memory(t *testing.T) {
	collagetest.CacheSuite(t, func(t *testing.T) collage.Cache {
		return cache.NewMemory(cache.MemoryConfig{})
	})
}

func TestSuite_Tiered(t *testing.T) {
	collagetest.CacheSuite(t, func(t *testing.T) collage.Cache {
		disk, err := cache.NewDisk(cache.DiskConfig{Dir: t.TempDir(), Version: "suite"})
		if err != nil {
			t.Fatal(err)
		}
		return cache.NewTiered(cache.NewMemory(cache.MemoryConfig{}), disk)
	})
}

func TestSuite_SharedMemory(t *testing.T) {
	collagetest.SharedCacheSuite(t, func(t *testing.T) (a, b collage.SharedCache) {
		c := cache.NewSharedMemory()
		return c, c
	})
}

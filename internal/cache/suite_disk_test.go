package cache_test

import (
	"testing"

	"github.com/Elagoht/collage/internal/cache"
	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

func TestSuite_Disk(t *testing.T) {
	collagetest.CacheSuite(t, func(t *testing.T) collage.Cache {
		disk, err := cache.NewDisk(cache.DiskConfig{Dir: t.TempDir(), Version: "suite"})
		if err != nil {
			t.Fatal(err)
		}
		return disk
	})
}

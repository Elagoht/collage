package core

import (
	"fmt"
	"log/slog"

	"github.com/Elagoht/collage/internal/cache"
)

// buildCache constructs the cache an application asked for.
//
// A caller-supplied Store wins over Type and is taken exactly as given: it is the
// one cache the application will use, so nothing here wraps, copies, or
// second-guesses it.
func buildCache(cfg Config, devMode bool, logger *slog.Logger) (cache.Cache, error) {
	if cfg.Cache.Store != nil {
		return cfg.Cache.Store, nil
	}

	memory := func() cache.Cache {
		return cache.NewMemory(cache.MemoryConfig{
			DefaultTTL: cfg.Cache.DefaultTTL,
			MaxEntries: cfg.Cache.MaxEntries,
		})
	}

	switch cfg.Cache.Type {
	case "", "memory":
		return memory(), nil

	case "disk":
		// Never on disk in development, whatever the configuration says.
		//
		// A disk cache outlives the process that filled it, and development is
		// exactly where the output changes between runs: an edited template, an
		// edited handler, and a page served from a previous build with nothing to
		// explain it. The version guard below would catch a released build; it
		// would not catch a developer who has not bumped anything, because nobody
		// bumps a version to save a file. In development the answer is memory.
		if devMode {
			logger.Info("collage: dev mode, using an in-memory cache instead of the configured disk cache")
			return memory(), nil
		}
		disk, err := cache.NewDisk(cache.DiskConfig{
			Dir:        cfg.Cache.Dir,
			Version:    cfg.Cache.Version,
			DefaultTTL: cfg.Cache.DefaultTTL,
		})
		if err != nil {
			return nil, err
		}
		logger.Info("collage: caching to disk", "dir", disk.Dir(), "version", cfg.Cache.Version)
		return disk, nil

	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedCache, cfg.Cache.Type)
	}
}

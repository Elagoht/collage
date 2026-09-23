package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/Elagoht/collage/internal/cache"
)

// buildCache constructs the cache an application asked for.
//
// A caller-supplied Store wins over Type and is taken exactly as given: it is the
// one cache the application will use, so nothing here wraps, copies, or
// second-guesses it.
func buildCache(cfg Config, devMode bool, csrfMarker string, logger *slog.Logger) (cache.Cache, error) {
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
		version := cfg.Cache.Version
		if version == "" {
			derived, err := buildFingerprint()
			if err != nil {
				// Without a fingerprint there is no safe way to tell this build's
				// output from the last one's, and serving the last one's is the
				// failure this whole mechanism exists to prevent. Memory is the
				// answer, not a guess.
				logger.Warn("collage: cannot identify this build, using an in-memory cache instead of the configured disk cache",
					"err", err)
				return memory(), nil
			}
			version = derived
		}

		// The forgery-token marker is part of what a stored body contains, and it
		// is derived from the application's key. A body written under one key
		// carries a marker the next key's substitution will not find, and would be
		// served with the marker still in it — a form refused on submission with
		// nothing to explain why. Changing the key therefore changes the
		// namespace, which is what this mixes in.
		if csrfMarker != "" {
			version += ":" + csrfMarker
			if len(cfg.Security.CSRFKey) == 0 {
				// Worth saying separately, because the consequence is not the one
				// the key warning describes: a generated key is different every
				// run, so the namespace is too, and nothing on disk is ever found
				// again. The cache still works; it just starts empty every time.
				logger.Warn("collage: no Security.CSRFKey set, so the disk cache starts empty after every restart")
			}
		}

		disk, err := cache.NewDisk(cache.DiskConfig{
			Dir:        cfg.Cache.Dir,
			Version:    version,
			DefaultTTL: cfg.Cache.DefaultTTL,
		})
		if err != nil {
			return nil, err
		}
		logger.Info("collage: caching to disk", "dir", disk.Dir(), "version", version)
		return disk, nil

	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedCache, cfg.Cache.Type)
	}
}

// buildFingerprint identifies the running build by hashing the executable.
//
// The executable's contents change exactly when the rendered output might: a
// changed template compiled in, a changed data handler, a changed dependency. A
// version string a human maintains changes when the human remembers, which is a
// different thing.
//
// It is also stable where it needs to be. Two invocations of an unchanged program
// hash the same, including under "go run", whose build cache hands back the same
// binary; and every machine in a fleet running the same build hashes the same, so
// they share a cache. Measured at one to two milliseconds for a binary of a few
// megabytes, paid once at startup.
//
// Not the VCS revision from debug.ReadBuildInfo: "go run" usually omits it, and it
// says nothing about uncommitted edits — which are exactly the edits a developer is
// looking at when a page comes back stale.
func buildFingerprint() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)[:16]), nil
}

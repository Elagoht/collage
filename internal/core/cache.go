package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

		// The forgery key is deliberately not part of the namespace. A body
		// stored under one key carries that key's marker, which the next key's
		// substitution would not find — but the response layer checks for exactly
		// that on every hit and treats such a body as a miss (see
		// httpx.Handler.cacheGet). Namespacing by the key instead emptied the
		// whole cache whenever the key changed, and on every restart of a site
		// with no key at all, forms or no forms.

		disk, err := cache.NewDisk(cache.DiskConfig{
			Dir:        cfg.Cache.Dir,
			Version:    version,
			DefaultTTL: cfg.Cache.DefaultTTL,
		})
		if errors.Is(err, cache.ErrEmptyCacheDir) || errors.Is(err, cache.ErrEmptyCacheVersion) {
			return nil, err
		}
		if err != nil {
			// A directory that cannot be created — a read-only filesystem, a
			// container with no writable working directory — is a slower site,
			// not a reason not to start one. Memory, and said so, as for a build
			// that cannot be identified.
			logger.Warn("collage: cannot use the disk cache directory, using an in-memory cache instead",
				"dir", cfg.Cache.Dir, "err", err)
			return memory(), nil
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

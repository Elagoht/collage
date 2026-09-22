package build

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// copyAssets copies every mount the application reports through Renderer.Mounts
// whose BuildCopy is true into outDirResolved, at
// "<OutDir>/<mount prefix><file name>", and returns the absolute paths written
// and any errors encountered.
//
// A mount's fs.FS is user-supplied — the embedding application can mount any
// implementation, including one whose fs.WalkDir yields an adversarial file name —
// so every resolved target is routed through the same documentTarget (and, through
// it, resolveTarget) plus verifyNoSymlinksBeneath containment check pages and
// documents use, rather than a second copy of that logic. A mount's own files are
// written to their literal path, the same shape a document is, since a static asset
// URL like "/static/app.css" must resolve to that exact file rather than a
// directory named after it.
//
// A failure resolving one file's target, or copying it, does not stop the rest of
// that mount, or the next mount, from being copied: it is recorded in the returned
// error slice and the walk continues, exactly like a failing page or document does
// not stop the rest of the build.
func (b *Builder) copyAssets(outDirResolved string) ([]string, []error) {
	var written []string
	var errs []error

	for _, mount := range b.app.Mounts() {
		if mount == nil || !mount.BuildCopy() {
			continue
		}
		fsys := mount.FS()

		walkErr := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				errs = append(errs, fmt.Errorf("collage: mount %q: walk %q: %w", mount.Prefix(), name, err))
				return nil
			}
			if d.IsDir() {
				return nil
			}

			urlPath := mount.Prefix() + name
			target, err := documentTarget(outDirResolved, urlPath)
			if err != nil {
				errs = append(errs, fmt.Errorf("collage: mount %q file %q: %w", mount.Prefix(), name, err))
				return nil
			}
			if err := verifyNoSymlinksBeneath(outDirResolved, target); err != nil {
				errs = append(errs, fmt.Errorf("collage: mount %q file %q: %w", mount.Prefix(), name, err))
				return nil
			}
			if err := copyMountFile(fsys, name, target); err != nil {
				errs = append(errs, fmt.Errorf("collage: mount %q file %q: %w", mount.Prefix(), name, err))
				return nil
			}
			written = append(written, target)
			return nil
		})
		if walkErr != nil {
			errs = append(errs, fmt.Errorf("collage: mount %q: %w", mount.Prefix(), walkErr))
		}
	}

	return written, errs
}

// copyMountFile copies the file at name within fsys to target, creating target's
// parent directories as needed.
func copyMountFile(fsys fs.FS, name, target string) error {
	src, err := fsys.Open(name)
	if err != nil {
		return fmt.Errorf("open %q: %w", name, err)
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create directory for %q: %w", target, err)
	}

	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %q: %w", target, err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy to %q: %w", target, err)
	}
	return nil
}

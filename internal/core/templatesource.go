package core

import (
	"io/fs"
	"log/slog"
	"os"
)

// templateSource decides which copy of the templates an application renders from.
//
// An application that embeds its templates with //go:embed carries them inside the
// binary, which is what lets it run from any working directory. In development that
// is exactly wrong: the embedded copy was fixed when the binary was built, so
// editing a template changes nothing until the process is restarted — and the
// engine's own DevMode reload cannot help, because reloading an embedded file
// reparses the same bytes.
//
// So in development, if the template root also exists on disk, that is what is
// rendered. The embed directive's path is a source-relative path, which is the same
// path a `go run .` in the package directory sees, so the two normally coincide —
// and when they do not, the directory simply is not there and the embedded copy is
// used, which is the safe way round.
//
// It returns the filesystem to render from, nil meaning "the disk directory named
// by root", and logs which copy was chosen: a developer editing a file that is not
// being read deserves to be told, and so does one who expected the embedded copy.
func templateSource(embedded fs.FS, root string, devMode bool, logger *slog.Logger) fs.FS {
	if !devMode || embedded == nil || root == "" {
		return embedded
	}

	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		logger.Debug("collage: dev mode is rendering the embedded templates; edits will not appear until the binary is rebuilt",
			"root", root)
		return embedded
	}

	logger.Info("collage: dev mode is rendering templates from disk, not the embedded copy",
		"root", root)
	return nil
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Elagoht/collage/internal/cli/plugincontract"
)

// goOutput runs the go tool in dir and returns what it printed, for the
// commands whose output is read rather than shown.
func (c *CLI) goOutput(ctx context.Context, dir string, args ...string) ([]byte, []byte, error) {
	var out, errOut bytes.Buffer
	err := c.runner().Run(ctx, dir, nil, &out, &errOut, "go", args...)
	return out.Bytes(), errOut.Bytes(), err
}

type moduleInfo struct {
	Path    string
	Version string
	Dir     string
}

// listModule is "go list -m -json path" in dir: where the module's source sits
// in the module cache, and at which version.
func (c *CLI) listModule(ctx context.Context, dir, path string) (moduleInfo, error) {
	out, errOut, err := c.goOutput(ctx, dir, "list", "-m", "-json", "--", path)
	if err != nil {
		return moduleInfo{}, goError(errOut, err)
	}
	var m moduleInfo
	if err := json.Unmarshal(out, &m); err != nil {
		return moduleInfo{}, err
	}
	return m, nil
}

// locateModule is listModule, with the module's source fetched when the
// module cache lacks it: go list -m does not download, and gives no Dir for a
// module required but not yet in the cache, as after a fresh clone.
func (c *CLI) locateModule(ctx context.Context, dir, path string) (moduleInfo, error) {
	info, err := c.listModule(ctx, dir, path)
	if err != nil || info.Dir != "" {
		return info, err
	}
	if info.Version == "" {
		return moduleInfo{}, fmt.Errorf("go list gave no version for %s", path)
	}
	return c.downloadModule(ctx, dir, info)
}

// entryManifest is the manifest of the module importPath, as the project at
// dir requires it. found is false when the module has no collage.json: an
// entry wired by hand, which has no name and requires nothing. An error says
// the manifest could not be read at all.
func (c *CLI) entryManifest(ctx context.Context, dir, importPath string) (m plugincontract.Manifest, found bool, err error) {
	info, err := c.locateModule(ctx, dir, importPath)
	if err != nil {
		return plugincontract.Manifest{}, false, err
	}
	if _, err := os.Stat(filepath.Join(info.Dir, "collage.json")); errors.Is(err, fs.ErrNotExist) {
		return plugincontract.Manifest{}, false, nil
	}
	m, err = plugincontract.ReadManifest(info.Dir)
	if err != nil {
		return plugincontract.Manifest{}, false, err
	}
	return m, true, nil
}

// downloadModule is "go mod download -json path@version" in dir: where the
// go tool put the module's source.
func (c *CLI) downloadModule(ctx context.Context, dir string, m moduleInfo) (moduleInfo, error) {
	out, errOut, err := c.goOutput(ctx, dir, "mod", "download", "-json", "--", m.Path+"@"+m.Version)
	if err != nil {
		return moduleInfo{}, goError(errOut, err)
	}
	var got moduleInfo
	if err := json.Unmarshal(out, &got); err != nil {
		return moduleInfo{}, err
	}
	if got.Dir == "" {
		return moduleInfo{}, fmt.Errorf("go mod download gave no directory for %s@%s", m.Path, m.Version)
	}
	return got, nil
}

// fileSnapshot is the content of files before a change, so a failed change
// can put them back: nil for a file that did not exist.
type fileSnapshot map[string][]byte

func snapshot(paths ...string) (fileSnapshot, error) {
	s := fileSnapshot{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			s[p] = nil
		case err != nil:
			return nil, err
		default:
			s[p] = b
		}
	}
	return s, nil
}

// restore writes every file back as it was, and removes those that were absent.
func (s fileSnapshot) restore() error {
	var errs []error
	for p, b := range s {
		if b == nil {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

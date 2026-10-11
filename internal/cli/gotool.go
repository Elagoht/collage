package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
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
		return moduleInfo{}, errors.New(string(bytes.TrimSpace(errOut)))
	}
	var m moduleInfo
	if err := json.Unmarshal(out, &m); err != nil {
		return moduleInfo{}, err
	}
	return m, nil
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

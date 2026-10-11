package plugincontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// Manifest is the part of a plugin's collage.json the CLI uses.
type Manifest struct {
	Name     string   `json:"name"`
	Requires []string `json:"requires"`
	Order    int      `json:"order"`
	Setup    string   `json:"setup"`
}

// manifestFile lists every top-level key the manifest schema allows, so an
// unknown one is refused as the schema's additionalProperties: false would.
type manifestFile struct {
	Schema            json.RawMessage `json:"$schema"`
	Name              string          `json:"name"`
	Description       json.RawMessage `json:"description"`
	Repository        json.RawMessage `json:"repository"`
	TemplateFunctions json.RawMessage `json:"templateFunctions"`
	Attributes        json.RawMessage `json:"attributes"`
	Snippets          json.RawMessage `json:"snippets"`
	Config            json.RawMessage `json:"config"`
	Requires          []string        `json:"requires"`
	Order             int             `json:"order"`
	Setup             string          `json:"setup"`
}

var name = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9-]*$`)

// ValidName reports whether s is a plugin name: "owner/name", lowercase.
func ValidName(s string) bool { return name.MatchString(s) }

// ReadManifest reads and checks dir/collage.json.
func ReadManifest(dir string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "collage.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, refuse("no collage.json at the module root")
	}
	if err != nil {
		return Manifest{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f manifestFile
	if err := dec.Decode(&f); err != nil {
		return Manifest{}, refuse(fmt.Sprintf("collage.json: %v", err))
	}
	if !ValidName(f.Name) {
		return Manifest{}, refuse(fmt.Sprintf("collage.json: name %q is not owner/name in lowercase letters, digits and dashes", f.Name))
	}
	for _, r := range f.Requires {
		if !ValidName(r) {
			return Manifest{}, refuse(fmt.Sprintf("collage.json: requires %q, which is not a plugin name", r))
		}
	}
	return Manifest{Name: f.Name, Requires: f.Requires, Order: f.Order, Setup: f.Setup}, nil
}

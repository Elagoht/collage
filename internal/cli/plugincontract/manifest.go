package plugincontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// Repository and TemplateFunctions are what "plugin add" tells the user
	// to read and to use; ReadManifest leaves either empty when the manifest
	// writes it in a shape it does not know.
	Repository        string             `json:"repository"`
	TemplateFunctions []TemplateFunction `json:"templateFunctions"`
}

// TemplateFunction is one template function a plugin's manifest lists.
type TemplateFunction struct {
	Signature string `json:"signature"`
	Doc       string `json:"doc"`
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
	m := Manifest{Name: f.Name, Requires: f.Requires, Order: f.Order, Setup: f.Setup}
	if json.Unmarshal(f.Repository, &m.Repository) != nil {
		m.Repository = ""
	}
	if json.Unmarshal(f.TemplateFunctions, &m.TemplateFunctions) != nil {
		m.TemplateFunctions = nil
	}
	if err := checkManifest(dec, m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// ReadManifestLenient reads dir/collage.json as a newer manifest schema may
// write it: unknown keys are ignored, and only name, requires and order are
// read. Malformed JSON and an invalid name are still refused. It is for the
// manifests of plugins already in a project, which a newer collage may have
// added; the plugin being added, removed or checked is read with ReadManifest.
func ReadManifestLenient(dir string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "collage.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, refuse("no collage.json at the module root")
	}
	if err != nil {
		return Manifest{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	var f struct {
		Name     string   `json:"name"`
		Requires []string `json:"requires"`
		Order    int      `json:"order"`
	}
	if err := dec.Decode(&f); err != nil {
		return Manifest{}, refuse(fmt.Sprintf("collage.json: %v", err))
	}
	m := Manifest{Name: f.Name, Requires: f.Requires, Order: f.Order}
	if err := checkManifest(dec, m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// checkManifest refuses text after the manifest's JSON value, and a name or
// requirement that is not a plugin name.
func checkManifest(dec *json.Decoder, m Manifest) error {
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return refuse("collage.json: more than one JSON value, or text after it")
	}
	if !ValidName(m.Name) {
		return refuse(fmt.Sprintf("collage.json: name %q is not owner/name in lowercase letters, digits and dashes", m.Name))
	}
	for _, r := range m.Requires {
		if !ValidName(r) {
			return refuse(fmt.Sprintf("collage.json: requires %q, which is not a plugin name", r))
		}
	}
	return nil
}

package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
)

// ConfigReader is what PluginConfig reads a plugin's configuration through. Its
// one method is unexported, so only a type embedding ConfigSource — collage's own
// hosts — can satisfy it.
type ConfigReader interface {
	pluginConfig() (section json.RawMessage, name string)
}

// ConfigSource is the plugin's name and the application's plugin
// configuration. A host embeds it to satisfy ConfigReader.
type ConfigSource struct {
	Name   string
	Config map[string]json.RawMessage
}

func (s ConfigSource) pluginConfig() (json.RawMessage, string) {
	return s.Config[s.Name], s.Name
}

// PluginConfig returns the plugin's configuration decoded over defaults. With no
// section — or an empty or null one — defaults come back unchanged; a section is
// decoded over a copy of them, so what it leaves out keeps its default. A map or
// slice inside defaults is filled in place, as decoding into a pointer always
// did. A malformed section is an error, and defaults come back with it: the
// operator wrote something, and running on defaults silently would hide it.
// What comes back depends on how it is malformed. A section that is not JSON
// leaves defaults untouched. A value of the wrong type fails while decoding, and
// decoding the rest goes on: defaults' plain fields come back as they were, but
// its maps may hold entries from the section and its slices' elements may be
// written over.
//
// A key the options do not have — at the top or in a nested object, and one
// named after a field tagged json:"-" — is an error naming the plugin and the
// key: a misspelt or renamed key would otherwise be ignored, and the plugin
// would run on its default without a word.
func PluginConfig[T any](r ConfigReader, defaults T) (T, error) {
	raw, name := r.pluginConfig()
	if len(raw) == 0 {
		return defaults, nil
	}
	out := defaults
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		if key, ok := unknownKey(err); ok {
			return defaults, fmt.Errorf("collage: %s: unknown key %q in plugins-config.json; remove it, or rename it to a key the plugin documents", name, key)
		}
		return defaults, fmt.Errorf("collage: plugin %q configuration: %w", name, err)
	}
	// Decode stops after one value, where Unmarshal refused anything after it.
	if _, err := dec.Token(); err != io.EOF {
		return defaults, fmt.Errorf("collage: plugin %q configuration: data after the section's value", name)
	}
	return out, nil
}

// unknownKeyMessage matches the error encoding/json gives a key
// DisallowUnknownFields refused, in its v1 wording and its v2 one.
var unknownKeyMessage = regexp.MustCompile(`unknown (?:field|object member name) "((?:[^"\\]|\\.)*)"`)

// unknownKey is the key err refuses, when err is encoding/json refusing one.
func unknownKey(err error) (string, bool) {
	m := unknownKeyMessage.FindStringSubmatch(err.Error())
	if m == nil {
		return "", false
	}
	return m[1], true
}

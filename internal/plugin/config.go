package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
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
// A value that fails to decode is named by its top-level key when the section is
// an object: `collage: elagoht/fail2ban: "banTime": …`.
//
// A key the options do not have — at the top or in a nested object, and one
// named after a field tagged json:"-" — is an error naming the plugin and the
// key (the key, not its path): a misspelt or renamed key would otherwise be
// ignored, and the plugin would run on its default without a word. Keys match
// fields case-insensitively, as encoding/json matches them, and a type with its
// own UnmarshalJSON decides about its own keys.
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
		if key, keyErr := failingMember[T](raw); keyErr != nil {
			return defaults, fmt.Errorf("collage: %s: %q: %w", name, key, keyErr)
		}
		return defaults, fmt.Errorf("collage: plugin %q configuration: %w", name, err)
	}
	// Decode stops after one value, where Unmarshal refused anything after it.
	if _, err := dec.Token(); err != io.EOF {
		return defaults, fmt.Errorf("collage: plugin %q configuration: data after the section's value", name)
	}
	return out, nil
}

// unknownKeyMessage is the whole of the error encoding/json gives a key
// DisallowUnknownFields refused: "json: unknown field" and the key, quoted as Go
// quotes a string.
var unknownKeyMessage = regexp.MustCompile(`^json: unknown field ("(?:[^"\\]|\\.)*")$`)

// unknownKey is the key err refuses, when err is encoding/json refusing one.
func unknownKey(err error) (string, bool) {
	m := unknownKeyMessage.FindStringSubmatch(err.Error())
	if m == nil {
		return "", false
	}
	key, unquoteErr := strconv.Unquote(m[1])
	if unquoteErr != nil {
		return "", false
	}
	return key, true
}

// failingMember finds the top-level member of the section raw whose value does
// not decode into T on its own, and the error it fails with, so the operator is
// told which key to fix rather than which Go field. Each member is decoded alone
// into a zero T, never into the defaults, whose maps a decode would write into.
// The error is nil when raw is not an object, or no single member fails.
func failingMember[T any](raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", nil
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return "", nil
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return "", nil
		}
		alone, err := json.Marshal(map[string]json.RawMessage{key: value})
		if err != nil {
			return "", nil
		}
		var probe T
		if err := json.Unmarshal(alone, &probe); err != nil {
			return key, err
		}
	}
	return "", nil
}

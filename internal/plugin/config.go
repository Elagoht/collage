package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
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
// decoded over a copy of them, so what it leaves out keeps its default. A
// top-level list the section has replaces the default list, element and all; a
// top-level map gets the section's entries added to a copy of the default's.
// Neither is written into the caller's memory. A malformed section is an error, and defaults come back with it: the
// operator wrote something, and running on defaults silently would hide it.
// What comes back depends on how it is malformed. A section that is not JSON
// leaves defaults untouched. A value of the wrong type fails while decoding, and
// decoding the rest goes on, into the copy: defaults come back with their
// top-level lists and maps as they were (a map inside a nested object may still
// hold entries from the section).
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
	out := detach(defaults, raw)
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

// detach copies defaults so decoding raw over the copy never writes into the
// caller's memory: a top-level slice the section has is dropped, so the section's
// list replaces it with new elements (encoding/json would otherwise decode each
// element over the default's, keeping fields the element leaves out), and a
// top-level map is cloned, so the section's entries join the defaults' in a map of
// the copy's own.
func detach[T any](defaults T, raw []byte) T {
	out := defaults
	v := reflect.ValueOf(&out).Elem()
	if v.Kind() != reflect.Struct {
		return out
	}
	var members map[string]json.RawMessage
	_ = json.Unmarshal(raw, &members) // not an object: Decode reports it
	for _, field := range reflect.VisibleFields(v.Type()) {
		if !field.IsExported() || field.Anonymous {
			continue
		}
		key := field.Name
		if tag, _, _ := strings.Cut(field.Tag.Get("json"), ","); tag == "-" {
			continue
		} else if tag != "" {
			key = tag
		}
		f, err := v.FieldByIndexErr(field.Index)
		if err != nil || !f.CanSet() {
			continue // behind a nil embedded pointer
		}
		switch f.Kind() {
		case reflect.Slice:
			if hasMember(members, key) {
				f.SetZero()
			}
		case reflect.Map:
			if !f.IsNil() {
				clone := reflect.MakeMapWithSize(f.Type(), f.Len())
				for it := f.MapRange(); it.Next(); {
					clone.SetMapIndex(it.Key(), it.Value())
				}
				f.Set(clone)
			}
		}
	}
	return out
}

// hasMember reports whether members has key, matched case-insensitively as
// encoding/json matches keys to fields.
func hasMember(members map[string]json.RawMessage, key string) bool {
	for name := range members {
		if strings.EqualFold(name, key) {
			return true
		}
	}
	return false
}

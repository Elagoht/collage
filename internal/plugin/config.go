package plugin

import (
	"encoding/json"
	"fmt"
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
func PluginConfig[T any](r ConfigReader, defaults T) (T, error) {
	raw, name := r.pluginConfig()
	if len(raw) == 0 {
		return defaults, nil
	}
	out := defaults
	if err := json.Unmarshal(raw, &out); err != nil {
		return defaults, fmt.Errorf("collage: plugin %q configuration: %w", name, err)
	}
	return out, nil
}

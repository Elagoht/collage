package collage

import (
	"encoding/json"
	"fmt"
	"os"
)

// LoadPluginConfig reads a JSON file of per-plugin configuration, keyed by plugin
// name, ready to hand to Config.PluginConfig:
//
//	{
//	  "elagoht/minimizer": {"html": true, "css": true, "js": true, "json": false},
//	  "elagoht/opti-image": {"allowedOrigins": [{"scheme": "https", "host": "images.example.com"}]}
//	}
//
// It is a convenience, not a requirement, and the framework never calls it. Nothing
// about plugin configuration is tied to JSON or to a file: Config.PluginConfig is a
// map the application fills however it likes — from YAML, from the environment, from
// constants in Go. An application whose configuration lives somewhere else is not
// shut out of plugins by this function's existence.
//
// A missing file is not an error. A deployment that configures no plugin should not
// have to create an empty file to say so, and every plugin's defaults already
// describe what "unconfigured" means.
func LoadPluginConfig(path string) (map[string]json.RawMessage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("collage: read plugin config %s: %w", path, err)
	}

	var config map[string]json.RawMessage
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("collage: parse plugin config %s: %w", path, err)
	}
	return config, nil
}

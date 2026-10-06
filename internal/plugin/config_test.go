package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

type limits struct {
	Limit  int               `json:"limit"`
	Window string            `json:"window"`
	Extra  map[string]string `json:"extra"`
}

func source(section string) ConfigSource {
	cfg := map[string]json.RawMessage{}
	if section != "" {
		cfg["test/p"] = json.RawMessage(section)
	}
	return ConfigSource{Name: "test/p", Config: cfg}
}

func TestPluginConfig(t *testing.T) {
	defaults := limits{Limit: 10, Window: "1m"}
	tests := []struct {
		name    string
		section string
		want    limits
		wantErr string
	}{
		{"no section", "", defaults, ""},
		{"null section", "null", defaults, ""},
		{"empty object", "{}", defaults, ""},
		{"partial overlay", `{"limit": 3}`, limits{Limit: 3, Window: "1m"}, ""},
		{"malformed", `{"limit": "three"}`, defaults, `collage: plugin "test/p" configuration`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := PluginConfig(source(test.section), defaults)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				if got.Limit != defaults.Limit || got.Window != defaults.Window {
					t.Errorf("on error got %+v, want the defaults", got)
				}
				return
			}
			if err != nil || got.Limit != test.want.Limit || got.Window != test.want.Window {
				t.Errorf("got %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
	if defaults.Limit != 10 {
		t.Error("PluginConfig changed the caller's defaults struct")
	}
}

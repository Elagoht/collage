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
		{"malformed", `{"window": "2m", "limit": "three"}`, defaults, `collage: test/p: "limit": json: cannot unmarshal`},
		{"not an object", `[1]`, defaults, `collage: plugin "test/p" configuration`},
		{"trailing data", `{"limit": 3} {"limit": 4}`, defaults, `collage: plugin "test/p" configuration`},
		{"unknown key", `{"limt": 3}`, defaults, `collage: test/p: unknown key "limt" in plugins-config.json`},
		{"unknown key with an escape", `{"li\u006et\"x": 3}`, defaults, `collage: test/p: unknown key "lint\"x" in plugins-config.json`},
		{"unknown nested key", `{"extra": {"a": "b"}, "window": "2m", "nope": 1}`, defaults, `unknown key "nope"`},
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

// A section that is not JSON is an error, and the defaults come back untouched —
// their map too, since nothing was decoded.
func TestPluginConfig_SyntaxError(t *testing.T) {
	defaults := limits{Limit: 10, Window: "1m", Extra: map[string]string{"a": "1"}}
	got, err := PluginConfig(source(`{"extra": {"b": "2"}, "limit": `), defaults)
	if err == nil || !strings.Contains(err.Error(), `collage: plugin "test/p" configuration`) {
		t.Fatalf("err = %v, want the plugin's configuration error", err)
	}
	if got.Limit != 10 || got.Window != "1m" || len(got.Extra) != 1 || got.Extra["a"] != "1" {
		t.Errorf("got %+v, want the defaults untouched", got)
	}
}

type font struct {
	Family string `json:"family"`
	File   string `json:"file"`
	Weight int    `json:"weight"`
}

type withLists struct {
	Fonts []font            `json:"fonts"`
	Extra map[string]string `json:"extra"`
	Embedded
}

type Embedded struct {
	Skip []string `json:"skip"`
}

// A list the section has replaces the default list: its elements are new values,
// not the defaults' elements written over, so a field an element leaves out is
// its zero value, and the caller's slices and maps are not touched.
func TestPluginConfig_ListsReplaceDefaults(t *testing.T) {
	defaults := withLists{
		Fonts:    []font{{Family: "A", File: "a.ttf", Weight: 700}},
		Extra:    map[string]string{"a": "1"},
		Embedded: Embedded{Skip: []string{"/a", "/b"}},
	}
	got, err := PluginConfig(source(`{"fonts":[{"family":"B","file":"b.ttf"}],"extra":{"b":"2"},"skip":["/c"]}`), defaults)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Fonts) != 1 || got.Fonts[0] != (font{Family: "B", File: "b.ttf"}) {
		t.Errorf("fonts = %+v, want only the section's font, weight unset", got.Fonts)
	}
	if len(got.Skip) != 1 || got.Skip[0] != "/c" {
		t.Errorf("skip = %v, want [/c]", got.Skip)
	}
	if got.Extra["a"] != "1" || got.Extra["b"] != "2" {
		t.Errorf("extra = %v, want the default entry and the section's", got.Extra)
	}
	if defaults.Fonts[0] != (font{Family: "A", File: "a.ttf", Weight: 700}) || defaults.Skip[0] != "/a" || len(defaults.Extra) != 1 {
		t.Errorf("the caller's defaults changed: %+v", defaults)
	}
}

// A malformed section leaves the caller's slices and maps as they were, too.
func TestPluginConfig_MalformedLeavesDefaultsListsAlone(t *testing.T) {
	defaults := withLists{Fonts: []font{{Family: "A"}}, Extra: map[string]string{"a": "1"}}
	if _, err := PluginConfig(source(`{"fonts":[{"family":"B"}],"extra":{"b":"2"},"skip":"x"}`), defaults); err == nil {
		t.Fatal("want an error")
	}
	if defaults.Fonts[0].Family != "A" || len(defaults.Extra) != 1 {
		t.Errorf("the caller's defaults changed: %+v", defaults)
	}
}

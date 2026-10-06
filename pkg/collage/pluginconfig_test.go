package collage_test

import (
	"context"
	"encoding/json"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

type configOpts struct {
	N int `json:"n"`
}

// configReader reads its configuration with collage.PluginConfig in both
// Configure (through a ConfigHost) and Init (through a Host).
type configReader struct {
	fromConfigure configOpts
	fromInit      configOpts
}

func (p *configReader) Name() string                   { return "test/cfg" }
func (p *configReader) Version() string                { return "0" }
func (p *configReader) Shutdown(context.Context) error { return nil }

func (p *configReader) Configure(_ context.Context, host collage.ConfigHost) error {
	cfg, err := collage.PluginConfig(host, configOpts{N: 1})
	p.fromConfigure = cfg
	return err
}

func (p *configReader) Init(_ context.Context, host collage.Host) error {
	cfg, err := collage.PluginConfig(host, configOpts{N: 1})
	p.fromInit = cfg
	return err
}

func newConfigApp(t *testing.T, p *configReader, section map[string]json.RawMessage) {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:       collage.ServerConfig{Host: "localhost", Port: 3000},
		Template:     collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte("<p>x</p>")}}, Root: "t"},
		Locale:       collage.LocaleConfig{Default: "en", Supported: []string{"en"}},
		Plugins:      []collage.Plugin{p},
		PluginConfig: section,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = app.Handler() // building the handler runs plugin Init
}

// TestPluginConfig_ReadsTheSectionFromBothHosts: a plugin's section reaches it
// through collage.PluginConfig from the ConfigHost in Configure and the Host in Init.
func TestPluginConfig_ReadsTheSectionFromBothHosts(t *testing.T) {
	p := &configReader{}
	newConfigApp(t, p, map[string]json.RawMessage{"test/cfg": json.RawMessage(`{"n": 5}`)})
	if p.fromConfigure.N != 5 {
		t.Errorf("PluginConfig in Configure: N = %d, want 5", p.fromConfigure.N)
	}
	if p.fromInit.N != 5 {
		t.Errorf("PluginConfig in Init: N = %d, want 5", p.fromInit.N)
	}
}

// TestPluginConfig_NoSectionKeepsDefaults: with no section the defaults come back.
func TestPluginConfig_NoSectionKeepsDefaults(t *testing.T) {
	p := &configReader{}
	newConfigApp(t, p, nil)
	if p.fromConfigure.N != 1 || p.fromInit.N != 1 {
		t.Errorf("PluginConfig with no section: Configure N = %d, Init N = %d, want 1 and 1", p.fromConfigure.N, p.fromInit.N)
	}
}

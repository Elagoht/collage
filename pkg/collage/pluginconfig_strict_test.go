package collage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

type strictOpts struct {
	Disallow []string `json:"disallow"`
	Limit    int      `json:"limit"`
	Secret   string   `json:"-"`
}

// strictPlugin reads strictOpts in Configure or in Init, and keeps what it read.
type strictPlugin struct {
	inConfigure bool
	got         strictOpts
}

var strictDefaults = strictOpts{Limit: 10, Secret: "kept"}

func (p *strictPlugin) Name() string                   { return "elagoht/robots" }
func (p *strictPlugin) Version() string                { return "0" }
func (p *strictPlugin) Shutdown(context.Context) error { return nil }
func (p *strictPlugin) Configure(_ context.Context, host collage.ConfigHost) error {
	if !p.inConfigure {
		return nil
	}
	var err error
	p.got, err = collage.PluginConfig(host, strictDefaults)
	return err
}
func (p *strictPlugin) Init(_ context.Context, host collage.Host) error {
	if p.inConfigure {
		return nil
	}
	var err error
	p.got, err = collage.PluginConfig(host, strictDefaults)
	return err
}

// A key the options do not have stops the application, naming the plugin and
// the key: a misspelt or renamed key is otherwise ignored, and the plugin runs
// on its default without a word. It holds in Configure as in Init.
func TestPluginConfig_UnknownKeyRefused(t *testing.T) {
	for _, inConfigure := range []bool{false, true} {
		_, err := collagetest.TrySite(t,
			collagetest.WithPlugins(&strictPlugin{inConfigure: inConfigure}),
			collagetest.WithPluginConfig("elagoht/robots", `{"dissallow":["/admin"]}`),
		)
		want := `elagoht/robots: unknown key "dissallow" in plugins-config.json`
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("configure=%v: start error = %v, want it to contain %s", inConfigure, err, want)
		}
	}
}

// A key named after a field the options keep out of JSON does nothing, so it is
// refused too; the field itself keeps its default.
func TestPluginConfig_IgnoredFieldKey(t *testing.T) {
	_, err := collagetest.TrySite(t,
		collagetest.WithPlugins(&strictPlugin{}),
		collagetest.WithPluginConfig("elagoht/robots", `{"Secret":"x"}`),
	)
	if err == nil || !strings.Contains(err.Error(), `unknown key "Secret"`) {
		t.Errorf("start error = %v, want the key refused", err)
	}
}

// Known keys still decode over the defaults, and a json:"-" field keeps its
// default.
func TestPluginConfig_KnownKeysDecode(t *testing.T) {
	p := &strictPlugin{}
	collagetest.NewSite(t,
		collagetest.WithPlugins(p),
		collagetest.WithPluginConfig("elagoht/robots", `{"disallow":["/admin"]}`),
	)
	if strings.Join(p.got.Disallow, ",") != "/admin" || p.got.Limit != 10 || p.got.Secret != "kept" {
		t.Errorf("got %+v", p.got)
	}
}

// An absent, empty or null section gives the defaults.
func TestPluginConfig_EmptySectionsKeepDefaults(t *testing.T) {
	for _, section := range []string{"", `{}`, `null`} {
		p := &strictPlugin{}
		var options []collagetest.SiteOption
		options = append(options, collagetest.WithPlugins(p))
		if section != "" {
			options = append(options, collagetest.WithPluginConfig("elagoht/robots", section))
		}
		collagetest.NewSite(t, options...)
		if p.got.Limit != 10 || p.got.Secret != "kept" || p.got.Disallow != nil {
			t.Errorf("section %q: got %+v, want the defaults", section, p.got)
		}
	}
}

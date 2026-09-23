package core

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/internal/types"
)

// extPlugin is a plugin that uses every capability the extension adds. It is one
// type rather than several because the point is that a single plugin can reach all
// of them, and because a plugin that implements Configurer *and* Plugin is the
// arrangement most likely to be wired up wrong.
type extPlugin struct {
	name string

	// what Configure captured
	configured   bool
	configDevOK  bool
	configErr    error
	settings     extSettings
	wrapCalls    int
	addFuncErr   error
	addFuncTwice error

	// what Init captured
	inited      bool
	initCfg     extSettings
	registerErr error
}

type extSettings struct {
	Greeting string `json:"greeting"`
	Enabled  bool   `json:"enabled"`
}

func (p *extPlugin) Name() string    { return p.name }
func (p *extPlugin) Version() string { return "1.0.0" }

func (p *extPlugin) Configure(_ context.Context, host plugin.ConfigHost) error {
	p.configured = true
	p.configDevOK = host.Logger() != nil

	// Defaults in, overlaid by whatever the application supplied.
	p.settings = extSettings{Greeting: "default"}
	p.configErr = host.Config(&p.settings)

	p.addFuncErr = host.AddTemplateFunc("shout", func(s string) string { return strings.ToUpper(s) })
	p.addFuncTwice = host.AddTemplateFunc("shout", func(s string) string { return s })

	host.WrapMount(func(inner fs.FS) fs.FS {
		p.wrapCalls++
		return inner
	})
	return nil
}

func (p *extPlugin) Init(_ context.Context, host plugin.Host) error {
	p.inited = true
	p.initCfg = extSettings{Greeting: "default"}
	_ = host.Config(&p.initCfg)

	p.registerErr = host.RegisterDocument(&types.Document{
		Name:        "plugin-doc",
		ContentType: "text/plain; charset=utf-8",
		Paths:       map[string]string{"en": "/plugin.txt"},
		Handler: func(context.Context, *types.RenderContext) ([]byte, []string, error) {
			return []byte("from the plugin\n"), nil, nil
		},
	})
	if p.registerErr != nil {
		return p.registerErr
	}
	return host.Mount("/plugin-assets/", fstest.MapFS{
		"x.txt": &fstest.MapFile{Data: []byte("mounted by a plugin")},
	})
}

func (p *extPlugin) Shutdown(context.Context) error { return nil }

func TestPlugin_ConfigureRunsBeforeTemplatesAndCanAddFunctions(t *testing.T) {
	p := &extPlugin{name: "acme/ext"}
	app := newTestApp(t, func(cfg *Config) {
		cfg.Plugins = []plugin.Plugin{p}
		cfg.PluginConfig = map[string]json.RawMessage{
			"acme/ext": json.RawMessage(`{"greeting":"hello","enabled":true}`),
		}
	})

	if !p.configured {
		t.Fatal("Configure never ran")
	}
	if p.configErr != nil {
		t.Fatalf("Config() = %v, want nil", p.configErr)
	}
	if p.settings.Greeting != "hello" || !p.settings.Enabled {
		t.Errorf("settings = %+v, want the application's section decoded over the defaults", p.settings)
	}
	if p.addFuncErr != nil {
		t.Errorf("AddTemplateFunc = %v, want nil", p.addFuncErr)
	}
	if !errors.Is(p.addFuncTwice, plugin.ErrDuplicateTemplateFunc) {
		t.Errorf("second AddTemplateFunc = %v, want ErrDuplicateTemplateFunc — two plugins overwriting each other's functions is a bug nobody would find from the output", p.addFuncTwice)
	}
	if app == nil {
		t.Fatal("New returned no application")
	}
}

func TestPlugin_ConfigDefaultsSurviveAnAbsentSection(t *testing.T) {
	// A plugin with no section keeps what it passed in. Zeroing it instead would
	// make "not configured" indistinguishable from "configured to the zero value".
	p := &extPlugin{name: "acme/ext"}
	newTestApp(t, func(cfg *Config) { cfg.Plugins = []plugin.Plugin{p} })

	if p.settings.Greeting != "default" {
		t.Errorf("greeting = %q, want the default the plugin passed in", p.settings.Greeting)
	}
}

func TestPlugin_UnknownConfigKeyIsAStartupError(t *testing.T) {
	// The typo case. Ignoring it leaves the operator certain a plugin was
	// configured while it ran on defaults.
	cfg := testConfig(writeTemplates(t, defaultTemplates()))
	cfg.Plugins = []plugin.Plugin{&extPlugin{name: "acme/ext"}}
	cfg.PluginConfig = map[string]json.RawMessage{
		"acme/exd": json.RawMessage(`{}`),
	}

	_, err := New(cfg)
	if !errors.Is(err, plugin.ErrUnknownPluginConfig) {
		t.Fatalf("New = %v, want ErrUnknownPluginConfig", err)
	}
}

func TestPlugin_ConfigurerRegisteredLateIsRefused(t *testing.T) {
	// Silently skipping Configure would leave a plugin that registered a template
	// function wondering why no template can call it.
	app := newTestApp(t, nil)

	err := app.RegisterPlugin(&extPlugin{name: "acme/ext"})
	if !errors.Is(err, ErrConfigurerRegisteredLate) {
		t.Fatalf("RegisterPlugin = %v, want ErrConfigurerRegisteredLate", err)
	}
}

func TestPlugin_ContributesADocumentAndAMount(t *testing.T) {
	p := &extPlugin{name: "acme/ext"}
	app := newTestApp(t, func(cfg *Config) { cfg.Plugins = []plugin.Plugin{p} })

	handler := app.Handler()
	if !p.inited {
		t.Fatal("Init never ran")
	}
	if p.registerErr != nil {
		t.Fatalf("RegisterDocument through Host = %v", p.registerErr)
	}

	for _, tc := range []struct{ path, want string }{
		{"/plugin.txt", "from the plugin"},
		{"/plugin-assets/x.txt", "mounted by a plugin"},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", tc.path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("GET %s body = %q, want it to contain %q", tc.path, rec.Body.String(), tc.want)
		}
	}
}

func TestPlugin_MountWrapperWrapsEveryFilesystem(t *testing.T) {
	p := &extPlugin{name: "acme/ext"}
	app := newTestApp(t, func(cfg *Config) { cfg.Plugins = []plugin.Plugin{p} })

	if err := app.Mount("/static/", fstest.MapFS{"a.css": &fstest.MapFile{Data: []byte("a{}")}}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	app.Handler()

	// The application's mount and the one the plugin registers from Init.
	if p.wrapCalls != 2 {
		t.Errorf("wrapper ran %d times, want 2 — every mounted filesystem, the plugin's own included", p.wrapCalls)
	}
}

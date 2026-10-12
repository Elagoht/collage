package collage_test

import (
	"context"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// A wrapper that embeds a Host (the pattern plugin tests use for spies) still
// answers origin questions: no type assertion is involved any more.
type spyHost struct{ collage.Host }

type originPlugin struct {
	multi  bool
	origin string
}

func (p *originPlugin) Name() string                   { return "test/origin" }
func (p *originPlugin) Version() string                { return "0" }
func (p *originPlugin) Shutdown(context.Context) error { return nil }
func (p *originPlugin) Init(ctx context.Context, host collage.Host) error {
	var h collage.Host = spyHost{host}
	p.multi = h.MultiOrigin()
	p.origin = h.OriginFor(ctx, "example.com")
	return nil
}

func TestHost_OriginThroughWrapper(t *testing.T) {
	cases := []struct {
		name       string
		resolver   bool
		wantOrigin string
		wantMulti  bool
	}{
		{name: "BaseURL only", wantOrigin: "https://example.com"},
		{name: "with an OriginResolver", resolver: true, wantOrigin: "https://tenant.example", wantMulti: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &originPlugin{}
			plugins := []collage.Plugin{p}
			if c.resolver {
				plugins = append(plugins, tenantResolver{})
			}
			app, err := collage.New(&collage.Config{
				BaseURL:  "https://example.com",
				Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
				Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte("<p>x</p>")}}, Root: "t"},
				Plugins:  plugins,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := app.Start(); err != nil {
				t.Fatal(err)
			}
			if p.origin != c.wantOrigin || p.multi != c.wantMulti {
				t.Errorf("origin %q multi %v, want %q %v", p.origin, p.multi, c.wantOrigin, c.wantMulti)
			}
		})
	}
}

// tenantResolver is an OriginResolver that answers every host with one origin.
type tenantResolver struct{}

func (tenantResolver) Name() string                             { return "test/tenant" }
func (tenantResolver) Version() string                          { return "0" }
func (tenantResolver) Init(context.Context, collage.Host) error { return nil }
func (tenantResolver) Shutdown(context.Context) error           { return nil }
func (tenantResolver) Origin(context.Context, string) (string, bool) {
	return "https://tenant.example", true
}

var _ collage.OriginResolver = tenantResolver{}

// originConfigurer records what ConfigHost's origin methods answer in Configure.
type originConfigurer struct {
	keepPlugin
	multi  bool
	origin string
}

func (p *originConfigurer) Configure(ctx context.Context, host collage.ConfigHost) error {
	p.multi = host.MultiOrigin()
	p.origin = host.OriginFor(ctx, "example.com")
	return nil
}

// ConfigHost's origin methods answer from a real Configure call, with and
// without an OriginResolver registered.
func TestConfigHost_OriginsInConfigure(t *testing.T) {
	cases := []struct {
		name       string
		resolver   bool
		wantOrigin string
		wantMulti  bool
	}{
		{name: "BaseURL only", wantOrigin: "https://example.com"},
		{name: "with an OriginResolver", resolver: true, wantOrigin: "https://tenant.example", wantMulti: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &originConfigurer{}
			plugins := []collage.Plugin{p}
			if c.resolver {
				plugins = append(plugins, tenantResolver{})
			}
			app, err := collage.New(&collage.Config{
				BaseURL:  "https://example.com",
				Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
				Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte("<p>x</p>")}}, Root: "t"},
				Plugins:  plugins,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := app.Start(); err != nil {
				t.Fatal(err)
			}
			if p.origin != c.wantOrigin || p.multi != c.wantMulti {
				t.Errorf("Configure saw origin %q multi %v, want %q %v", p.origin, p.multi, c.wantOrigin, c.wantMulti)
			}
		})
	}
}

// Review Focus 1: a plugin using only names that stay compiles against Host.
type keepPlugin struct{}

func (keepPlugin) Name() string                   { return "test/keep" }
func (keepPlugin) Version() string                { return "0" }
func (keepPlugin) Shutdown(context.Context) error { return nil }
func (keepPlugin) Init(_ context.Context, host collage.Host) error {
	host.Logger().Info("hi")
	return host.Use(func(h http.Handler) http.Handler { return h })
}

func TestHost_ExternalPluginCompiles(t *testing.T) {
	var _ collage.Plugin = keepPlugin{}
}

func TestHost_MethodSet(t *testing.T) {
	want := []string{"BaseURL", "BuildID", "DevMode", "Handle", "InvalidateTags", "Locales", "Logger",
		"Mount", "MultiOrigin", "OriginFor", "Page", "PageURLs", "Pages", "RegisterCommand",
		"RegisterDocument", "RenderFragment", "ServeStatus", "URL", "Use"}
	typ := reflect.TypeOf((*collage.Host)(nil)).Elem()
	var got []string
	for i := range typ.NumMethod() {
		if m := typ.Method(i); m.IsExported() {
			got = append(got, m.Name)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("Host methods =\n%v\nwant\n%v", got, want)
	}
}

// ConfigHost answers the same origin questions Host does.
func TestConfigHost_Origins(t *testing.T) {
	typ := reflect.TypeOf((*collage.ConfigHost)(nil)).Elem()
	for _, name := range []string{"OriginFor", "MultiOrigin"} {
		if _, ok := typ.MethodByName(name); !ok {
			t.Errorf("ConfigHost has no %s", name)
		}
	}
	if _, ok := typ.MethodByName("Dynamic"); ok {
		t.Error("ConfigHost still has Dynamic")
	}
}

// Configurer is found by type assertion, so a ConfigHost literal that drifted
// from the framework's own would silently stop Configure being called rather
// than fail to build. This pins it at compile time.
type configurePlugin struct{ keepPlugin }

func (configurePlugin) Configure(context.Context, collage.ConfigHost) error { return nil }

var _ collage.Configurer = configurePlugin{}

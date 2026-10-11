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
	p := &originPlugin{}
	app, err := collage.New(&collage.Config{
		BaseURL:  "https://example.com",
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte("<p>x</p>")}}, Root: "t"},
		Plugins:  []collage.Plugin{p},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	if p.origin != "https://example.com" || p.multi {
		t.Errorf("origin %q multi %v", p.origin, p.multi)
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

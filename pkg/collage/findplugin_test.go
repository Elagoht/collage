package collage_test

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/internal/plugin"
	"github.com/Elagoht/collage/pkg/collage"
)

// newStartedApp builds an application over plugins and starts it, which runs
// Configure and Init. It needs one template to build, as every application does.
func newStartedApp(t *testing.T, plugins ...collage.Plugin) {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte("<p>x</p>")}}, Root: "t"},
		Locale:   collage.LocaleConfig{Default: "en", Supported: []string{"en"}},
		Plugins:  plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil { // Init runs when the application starts
		t.Fatal(err)
	}
}

type carrier struct{ name string }

func (c *carrier) Name() string                             { return c.name }
func (c *carrier) Version() string                          { return "0" }
func (c *carrier) Init(context.Context, collage.Host) error { return nil }
func (c *carrier) Shutdown(context.Context) error           { return nil }

// seeker looks for a *carrier in Configure and in Init, and keeps what it found.
type seeker struct {
	inConfigure, inInit *carrier
}

func (s *seeker) Name() string    { return "test/seeker" }
func (s *seeker) Version() string { return "0" }
func (s *seeker) Configure(_ context.Context, host collage.ConfigHost) error {
	s.inConfigure, _ = collage.FindPlugin[*carrier](host)
	return nil
}
func (s *seeker) Init(_ context.Context, host collage.Host) error {
	s.inInit, _ = collage.FindPlugin[*carrier](host)
	return nil
}
func (s *seeker) Shutdown(context.Context) error { return nil }

// A plugin finds another by type, in Configure and in Init, wherever the other
// sits in the list: listed after it, it is found before it was configured.
func TestFindPlugin(t *testing.T) {
	c := &carrier{name: "test/carrier"}
	s := &seeker{}
	newStartedApp(t, s, c)
	if s.inConfigure != c || s.inInit != c {
		t.Fatalf("Configure found %p, Init found %p, want %p", s.inConfigure, s.inInit, c)
	}
}

// No plugin of the type: the zero value and false.
func TestFindPlugin_None(t *testing.T) {
	s := &seeker{}
	newStartedApp(t, s)
	if s.inConfigure != nil || s.inInit != nil {
		t.Fatal("found a carrier that is not there")
	}
}

// A host collage did not make (a test double) has no plugins to offer.
// plugin.ConfigSource satisfies ConfigReader on its own, as a test double does.
func TestFindPlugin_ForeignHost(t *testing.T) {
	if _, ok := collage.FindPlugin[*carrier](plugin.ConfigSource{Name: "x/y"}); ok {
		t.Fatal("found a plugin through a host with none")
	}
}

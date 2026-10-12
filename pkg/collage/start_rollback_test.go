package collage_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// lifecyclePlugin counts its Init and Shutdown calls, and fails Init when told to.
type lifecyclePlugin struct {
	name     string
	fail     error
	inits    atomic.Int32
	shutdown atomic.Int32
}

func (p *lifecyclePlugin) Name() string    { return p.name }
func (p *lifecyclePlugin) Version() string { return "0" }
func (p *lifecyclePlugin) Init(context.Context, collage.Host) error {
	p.inits.Add(1)
	return p.fail
}
func (p *lifecyclePlugin) Shutdown(context.Context) error {
	p.shutdown.Add(1)
	return nil
}

func rollbackApp(t *testing.T, plugins ...collage.Plugin) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
		Plugins:  plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// A start that fails after every plugin's Init succeeded shuts them down, once:
// they are not left running, and a later Shutdown does not shut them down again.
func TestStart_FailureAfterInitShutsPluginsDownOnce(t *testing.T) {
	a := &lifecyclePlugin{name: "test/a"}
	app := rollbackApp(t, a)
	page := collage.NewPage("doc").WithContent(collage.NewFragment("doc", "p.html").Build()).WithPath("en", "/docs/x").Build()
	if err := app.Register(page); err != nil {
		t.Fatal(err)
	}
	if err := app.Mount("/docs/", fstest.MapFS{"a.txt": {Data: []byte("a")}}); err != nil {
		t.Fatal(err)
	}

	if err := app.Start(); !errors.Is(err, collage.ErrMountShadowsRoute) {
		t.Fatalf("Start = %v, want ErrMountShadowsRoute", err)
	}
	if a.inits.Load() != 1 || a.shutdown.Load() != 1 {
		t.Errorf("after the failed start: Init %d, Shutdown %d; want 1 and 1", a.inits.Load(), a.shutdown.Load())
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown = %v", err)
	}
	if a.shutdown.Load() != 1 {
		t.Errorf("Shutdown after the failed start shut the plugin down again: %d calls", a.shutdown.Load())
	}
}

// A plugin's Init failing rolls back the ones before it, and a later Shutdown
// shuts down neither them again nor the plugin that never finished Init.
func TestStart_InitFailureThenShutdownAddsNothing(t *testing.T) {
	a := &lifecyclePlugin{name: "test/a"}
	b := &lifecyclePlugin{name: "test/b", fail: errors.New("boom")}
	app := rollbackApp(t, a, b)

	if err := app.Start(); err == nil {
		t.Fatal("Start succeeded with a failing Init")
	}
	if a.shutdown.Load() != 1 || b.shutdown.Load() != 0 {
		t.Errorf("after rollback: a Shutdown %d, b Shutdown %d; want 1 and 0", a.shutdown.Load(), b.shutdown.Load())
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown = %v", err)
	}
	if a.shutdown.Load() != 1 || b.shutdown.Load() != 0 {
		t.Errorf("after Shutdown: a Shutdown %d, b Shutdown %d; want 1 and 0", a.shutdown.Load(), b.shutdown.Load())
	}
}

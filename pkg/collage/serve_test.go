package collage

import (
	"context"
	"sync/atomic"
	"testing"
)

// buildServeSpy counts OnServe calls.
type buildServeSpy struct {
	inits  atomic.Int32
	served atomic.Int32
}

func (*buildServeSpy) Name() string                       { return "serve-spy" }
func (*buildServeSpy) Version() string                    { return "1.0.0" }
func (s *buildServeSpy) Init(context.Context, Host) error { s.inits.Add(1); return nil }
func (*buildServeSpy) Shutdown(context.Context) error     { return nil }
func (s *buildServeSpy) OnServe(context.Context)          { s.served.Add(1) }

var _ ServeHook = (*buildServeSpy)(nil)

// A static build runs plugin Init but serves nothing, so OnServe never runs:
// a scheduler must not fire from collage build.
func TestServe_NotCalledInBuild(t *testing.T) {
	spy := &buildServeSpy{}
	app := buildTestApp(t)
	if err := app.RegisterPlugin(spy); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	builder, err := NewBuilder(app, BuildOptions{OutDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	if _, err := builder.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if spy.inits.Load() == 0 {
		t.Fatal("the spy's Init never ran, so its OnServe count proves nothing")
	}
	if n := spy.served.Load(); n != 0 {
		t.Fatalf("OnServe called %d times in a build, want 0", n)
	}
}

package plugin

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Elagoht/collage/internal/asset"
	"github.com/Elagoht/collage/internal/types"
)

// errPluginPanic is what a panicking test plugin panics with. Using a fixed sentinel
// value (rather than any) keeps the tests within the project's no-any rule, which
// applies to tests too.
var errPluginPanic = errors.New("collage: test plugin panic")

// errTestHook is returned by a hook to simulate a plugin's own failure.
var errTestHook = errors.New("collage: test hook failure")

// callLog records call names in order, safe for concurrent use by the -race tests.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, name)
}

func (l *callLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// testPlugin implements Plugin only — no hook interfaces — so it doubles as the
// "implements no hooks" fixture.
type testPlugin struct {
	name    string
	version string
	log     *callLog

	initErr        error
	initPanics     bool
	shutdownErr    error
	shutdownPanics bool
}

func (p *testPlugin) Name() string    { return p.name }
func (p *testPlugin) Version() string { return p.version }

func (p *testPlugin) Init(ctx context.Context, host Host) error {
	if p.log != nil {
		p.log.add("init:" + p.name)
	}
	if p.initPanics {
		panic(errPluginPanic)
	}
	return p.initErr
}

func (p *testPlugin) Shutdown(ctx context.Context) error {
	if p.log != nil {
		p.log.add("shutdown:" + p.name)
	}
	if p.shutdownPanics {
		panic(errPluginPanic)
	}
	return p.shutdownErr
}

var _ Plugin = (*testPlugin)(nil)

// hookPlugin implements every hook interface on top of testPlugin, recording each
// call to the shared log and letting a test configure failure or mutation
// behaviour per hook.
type hookPlugin struct {
	testPlugin

	pageResolvedErr    error
	pageResolvedPanics bool

	beforeRenderErr    error
	beforeRenderPanics bool

	afterRenderErr    error
	afterRenderPanics bool
	afterRenderMutate func(ev *AfterRenderEvent)

	cacheWriteErr    error
	cacheWritePanics bool
	cacheWriteMutate func(ev *CacheWriteEvent)

	cacheInvalidateErr    error
	cacheInvalidatePanics bool

	errorErr    error
	errorPanics bool
}

func (p *hookPlugin) OnPageResolved(ctx context.Context, ev *PageResolvedEvent) error {
	p.log.add("OnPageResolved:" + p.name)
	if p.pageResolvedPanics {
		panic(errPluginPanic)
	}
	return p.pageResolvedErr
}

func (p *hookPlugin) OnBeforeRender(ctx context.Context, ev *BeforeRenderEvent) error {
	p.log.add("OnBeforeRender:" + p.name)
	if p.beforeRenderPanics {
		panic(errPluginPanic)
	}
	return p.beforeRenderErr
}

func (p *hookPlugin) OnAfterRender(ctx context.Context, ev *AfterRenderEvent) error {
	p.log.add("OnAfterRender:" + p.name)
	if p.afterRenderPanics {
		panic(errPluginPanic)
	}
	if p.afterRenderMutate != nil {
		p.afterRenderMutate(ev)
	}
	return p.afterRenderErr
}

func (p *hookPlugin) OnCacheWrite(ctx context.Context, ev *CacheWriteEvent) error {
	p.log.add("OnCacheWrite:" + p.name)
	if p.cacheWritePanics {
		panic(errPluginPanic)
	}
	if p.cacheWriteMutate != nil {
		p.cacheWriteMutate(ev)
	}
	return p.cacheWriteErr
}

func (p *hookPlugin) OnCacheInvalidate(ctx context.Context, ev *CacheInvalidateEvent) error {
	p.log.add("OnCacheInvalidate:" + p.name)
	if p.cacheInvalidatePanics {
		panic(errPluginPanic)
	}
	return p.cacheInvalidateErr
}

func (p *hookPlugin) OnError(ctx context.Context, ev *ErrorEvent) error {
	p.log.add("OnError:" + p.name)
	if p.errorPanics {
		panic(errPluginPanic)
	}
	return p.errorErr
}

var (
	_ PageResolvedHook    = (*hookPlugin)(nil)
	_ BeforeRenderHook    = (*hookPlugin)(nil)
	_ AfterRenderHook     = (*hookPlugin)(nil)
	_ CacheWriteHook      = (*hookPlugin)(nil)
	_ CacheInvalidateHook = (*hookPlugin)(nil)
	_ ErrorHook           = (*hookPlugin)(nil)
)

func newHookPlugin(name string, log *callLog) *hookPlugin {
	return &hookPlugin{testPlugin: testPlugin{name: name, version: "v1", log: log}}
}

func TestRegistry_Register(t *testing.T) {
	t.Run("registers in order and Plugins reports it", func(t *testing.T) {
		r := NewRegistry(nil)
		a := &testPlugin{name: "a"}
		b := &testPlugin{name: "b"}
		if err := r.Register(a); err != nil {
			t.Fatalf("Register(a) error = %v, want nil", err)
		}
		if err := r.Register(b); err != nil {
			t.Fatalf("Register(b) error = %v, want nil", err)
		}
		got := r.Plugins()
		if len(got) != 2 || got[0].Name() != "a" || got[1].Name() != "b" {
			t.Fatalf("Plugins() = %v, want [a b] in order", got)
		}
	})

	t.Run("Plugins returns a copy", func(t *testing.T) {
		r := NewRegistry(nil)
		if err := r.Register(&testPlugin{name: "a"}); err != nil {
			t.Fatalf("Register() error = %v, want nil", err)
		}
		got := r.Plugins()
		got[0] = &testPlugin{name: "mutated"}
		again := r.Plugins()
		if again[0].Name() != "a" {
			t.Fatalf("mutating Plugins() result affected the registry: got %q, want %q", again[0].Name(), "a")
		}
	})

	t.Run("duplicate name is rejected", func(t *testing.T) {
		r := NewRegistry(nil)
		if err := r.Register(&testPlugin{name: "dup"}); err != nil {
			t.Fatalf("first Register() error = %v, want nil", err)
		}
		err := r.Register(&testPlugin{name: "dup"})
		if !errors.Is(err, ErrDuplicatePlugin) {
			t.Fatalf("second Register() error = %v, want ErrDuplicatePlugin", err)
		}
		if len(r.Plugins()) != 1 {
			t.Fatalf("Plugins() length = %d, want 1 (rejected registration must not be added)", len(r.Plugins()))
		}
	})

	t.Run("empty name is rejected", func(t *testing.T) {
		r := NewRegistry(nil)
		err := r.Register(&testPlugin{name: ""})
		if !errors.Is(err, ErrEmptyPluginName) {
			t.Fatalf("Register() error = %v, want ErrEmptyPluginName", err)
		}
	})

	t.Run("nil plugin is rejected", func(t *testing.T) {
		r := NewRegistry(nil)
		if err := r.Register(nil); !errors.Is(err, ErrNilPlugin) {
			t.Fatalf("Register(nil) error = %v, want ErrNilPlugin", err)
		}
	})

	t.Run("nil registry is rejected", func(t *testing.T) {
		var r *Registry
		if err := r.Register(&testPlugin{name: "a"}); !errors.Is(err, ErrNilRegistry) {
			t.Fatalf("Register() on nil *Registry error = %v, want ErrNilRegistry", err)
		}
	})

	t.Run("registering after Init is rejected", func(t *testing.T) {
		r := NewRegistry(nil)
		if err := r.Register(&testPlugin{name: "a"}); err != nil {
			t.Fatalf("Register() error = %v, want nil", err)
		}
		if err := r.Init(context.Background(), hostFor); err != nil {
			t.Fatalf("Init() error = %v, want nil", err)
		}
		err := r.Register(&testPlugin{name: "b"})
		if !errors.Is(err, ErrRegistryStarted) {
			t.Fatalf("Register() after Init error = %v, want ErrRegistryStarted", err)
		}
	})
}

func TestRegistry_Init(t *testing.T) {
	t.Run("calls Init in registration order", func(t *testing.T) {
		log := &callLog{}
		r := NewRegistry(nil)
		mustRegister(t, r, &testPlugin{name: "a", log: log})
		mustRegister(t, r, &testPlugin{name: "b", log: log})
		mustRegister(t, r, &testPlugin{name: "c", log: log})

		if err := r.Init(context.Background(), hostFor); err != nil {
			t.Fatalf("Init() error = %v, want nil", err)
		}

		want := []string{"init:a", "init:b", "init:c"}
		if got := log.get(); !equalStrings(got, want) {
			t.Fatalf("Init() call order = %v, want %v", got, want)
		}
	})

	t.Run("a failure rolls back already-initialised plugins in reverse order", func(t *testing.T) {
		log := &callLog{}
		errBoom := errors.New("collage: test init failure")
		r := NewRegistry(nil)
		mustRegister(t, r, &testPlugin{name: "a", log: log})
		mustRegister(t, r, &testPlugin{name: "b", log: log})
		mustRegister(t, r, &testPlugin{name: "c", log: log, initErr: errBoom})
		mustRegister(t, r, &testPlugin{name: "d", log: log})

		err := r.Init(context.Background(), hostFor)
		if err == nil {
			t.Fatal("Init() error = nil, want non-nil")
		}
		if !errors.Is(err, errBoom) {
			t.Fatalf("Init() error = %v, does not wrap the original failure", err)
		}
		if !strings.Contains(err.Error(), `"c"`) {
			t.Fatalf("Init() error = %v, want it to name plugin %q", err, "c")
		}

		// d never finished Init and must not be shut down; a and b must be, in
		// reverse of their init order; c's own Init failure means it is never
		// shut down either.
		want := []string{"init:a", "init:b", "init:c", "shutdown:b", "shutdown:a"}
		if got := log.get(); !equalStrings(got, want) {
			t.Fatalf("Init() rollback call order = %v, want %v", got, want)
		}
	})

	t.Run("a rollback Shutdown error is joined with the original Init error", func(t *testing.T) {
		log := &callLog{}
		errInit := errors.New("collage: test init failure")
		errShutdown := errors.New("collage: test shutdown failure")
		r := NewRegistry(nil)
		mustRegister(t, r, &testPlugin{name: "a", log: log, shutdownErr: errShutdown})
		mustRegister(t, r, &testPlugin{name: "b", log: log, initErr: errInit})

		err := r.Init(context.Background(), hostFor)
		if !errors.Is(err, errInit) {
			t.Fatalf("Init() error = %v, want it to wrap the init failure", err)
		}
		if !errors.Is(err, errShutdown) {
			t.Fatalf("Init() error = %v, want it to also wrap the rollback shutdown failure", err)
		}
	})

	t.Run("a panicking Init is contained, named, and still rolls back", func(t *testing.T) {
		log := &callLog{}
		r := NewRegistry(nil)
		mustRegister(t, r, &testPlugin{name: "a", log: log})
		mustRegister(t, r, &testPlugin{name: "panicky", log: log, initPanics: true})

		err := r.Init(context.Background(), hostFor)
		if err == nil {
			t.Fatal("Init() error = nil, want non-nil for a panicking plugin")
		}
		if !strings.Contains(err.Error(), "panicky") {
			t.Fatalf("Init() error = %v, want it to name the panicking plugin", err)
		}
		want := []string{"init:a", "init:panicky", "shutdown:a"}
		if got := log.get(); !equalStrings(got, want) {
			t.Fatalf("Init() call order = %v, want %v", got, want)
		}
	})

	t.Run("nil registry is a no-op", func(t *testing.T) {
		var r *Registry
		if err := r.Init(context.Background(), hostFor); err != nil {
			t.Fatalf("Init() on nil *Registry error = %v, want nil", err)
		}
	})

	t.Run("empty registry is a no-op", func(t *testing.T) {
		r := NewRegistry(nil)
		if err := r.Init(context.Background(), hostFor); err != nil {
			t.Fatalf("Init() on empty Registry error = %v, want nil", err)
		}
	})
}

func TestRegistry_Shutdown(t *testing.T) {
	t.Run("reverse registration order", func(t *testing.T) {
		log := &callLog{}
		r := NewRegistry(nil)
		mustRegister(t, r, &testPlugin{name: "a", log: log})
		mustRegister(t, r, &testPlugin{name: "b", log: log})
		mustRegister(t, r, &testPlugin{name: "c", log: log})
		if err := r.Init(context.Background(), hostFor); err != nil {
			t.Fatalf("Init() error = %v, want nil", err)
		}
		log.calls = nil // discard the Init calls, keep only Shutdown's

		if err := r.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown() error = %v, want nil", err)
		}
		want := []string{"shutdown:c", "shutdown:b", "shutdown:a"}
		if got := log.get(); !equalStrings(got, want) {
			t.Fatalf("Shutdown() call order = %v, want %v", got, want)
		}
	})

	t.Run("never stops early and joins every error", func(t *testing.T) {
		log := &callLog{}
		errFirst := errors.New("collage: test shutdown failure one")
		errThird := errors.New("collage: test shutdown failure two")
		r := NewRegistry(nil)
		mustRegister(t, r, &testPlugin{name: "a", log: log, shutdownErr: errFirst})
		mustRegister(t, r, &testPlugin{name: "panicky", log: log, shutdownPanics: true})
		mustRegister(t, r, &testPlugin{name: "c", log: log, shutdownErr: errThird})

		err := r.Shutdown(context.Background())
		if err == nil {
			t.Fatal("Shutdown() error = nil, want non-nil")
		}
		if !errors.Is(err, errFirst) {
			t.Fatalf("Shutdown() error = %v, want it to wrap errFirst", err)
		}
		if !errors.Is(err, errThird) {
			t.Fatalf("Shutdown() error = %v, want it to wrap errThird", err)
		}
		if !strings.Contains(err.Error(), `"panicky"`) {
			t.Fatalf("Shutdown() error = %v, want it to name the panicking plugin", err)
		}
		// All three must run even though the middle one panicked.
		want := []string{"shutdown:c", "shutdown:panicky", "shutdown:a"}
		if got := log.get(); !equalStrings(got, want) {
			t.Fatalf("Shutdown() call order = %v, want %v", got, want)
		}
	})

	t.Run("nil registry is a no-op", func(t *testing.T) {
		var r *Registry
		if err := r.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown() on nil *Registry error = %v, want nil", err)
		}
	})

	t.Run("empty registry is a no-op", func(t *testing.T) {
		r := NewRegistry(nil)
		if err := r.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown() on empty Registry error = %v, want nil", err)
		}
	})
}

func TestRegistry_PluginWithNoHooksIsNeverCalled(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	plain := &testPlugin{name: "plain", log: log}
	hooked := newHookPlugin("hooked", log)
	mustRegister(t, r, plain)
	mustRegister(t, r, hooked)

	ctx := context.Background()
	if err := r.PageResolved(ctx, &PageResolvedEvent{}); err != nil {
		t.Fatalf("PageResolved() error = %v, want nil", err)
	}
	if err := r.BeforeRender(ctx, &BeforeRenderEvent{}); err != nil {
		t.Fatalf("BeforeRender() error = %v, want nil", err)
	}
	if err := r.AfterRender(ctx, &AfterRenderEvent{}); err != nil {
		t.Fatalf("AfterRender() error = %v, want nil", err)
	}
	if err := r.CacheWrite(ctx, &CacheWriteEvent{}); err != nil {
		t.Fatalf("CacheWrite() error = %v, want nil", err)
	}
	if err := r.CacheInvalidate(ctx, &CacheInvalidateEvent{}); err != nil {
		t.Fatalf("CacheInvalidate() error = %v, want nil", err)
	}
	if err := r.Error(ctx, &ErrorEvent{}); err != nil {
		t.Fatalf("Error() error = %v, want nil", err)
	}

	for _, call := range log.get() {
		if strings.Contains(call, "plain") {
			t.Fatalf("plugin implementing no hooks was called: %q", call)
		}
	}
	if len(log.get()) != 6 {
		t.Fatalf("hooked plugin call count = %d, want 6 (one per hook)", len(log.get()))
	}
}

// fakeHost is a minimal Host implementation for tests that only need to satisfy
// the parameter, not exercise it.
type fakeHost struct{}

func (fakeHost) DevMode() bool                                               { return false }
func (fakeHost) Pages() []*types.Page                                        { return nil }
func (fakeHost) Page(name string) (*types.Page, bool)                        { return nil, false }
func (fakeHost) InvalidateTags(ctx context.Context, tags ...string) error    { return nil }
func (fakeHost) Logger() *slog.Logger                                        { return slog.Default() }
func (fakeHost) RegisterCommand(cmd Command) error                           { return nil }
func (fakeHost) Config(v any) error                                          { return nil } // any: restates encoding/json's own parameter type
func (fakeHost) RegisterPage(page *types.Page) error                         { return nil }
func (fakeHost) RegisterDocument(doc *types.Document) error                  { return nil }
func (fakeHost) Mount(prefix string, fsys fs.FS, opts ...asset.Option) error { return nil }
func (fakeHost) Handle(prefix string, handler http.Handler) error            { return nil }
func (fakeHost) RenderFragment(*http.Request, FragmentRequest) (*FragmentRender, error) {
	return nil, nil
}

// hostFor is what Registry.Init takes: one host per plugin, chosen by name.
func hostFor(string) Host { return fakeHost{} }

var _ Host = fakeHost{}

func mustRegister(t *testing.T, r *Registry, p Plugin) {
	t.Helper()
	if err := r.Register(p); err != nil {
		t.Fatalf("Register(%q) error = %v, want nil", p.Name(), err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// streamPlugin closes its streams when asked, or panics doing it.
type streamPlugin struct {
	testPlugin
	panics bool
}

func (p *streamPlugin) CloseStreams() {
	if p.panics {
		panic(errPluginPanic)
	}
	p.log.add(p.name + ".CloseStreams")
}

// Every stream closer is asked, in order, and one that panics does not keep the
// next from closing its own.
func TestRegistry_CloseStreams(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	for _, p := range []Plugin{
		&streamPlugin{testPlugin: testPlugin{name: "a", log: log}},
		&streamPlugin{testPlugin: testPlugin{name: "b", log: log}, panics: true},
		&testPlugin{name: "c", log: log},
		&streamPlugin{testPlugin: testPlugin{name: "d", log: log}},
	} {
		if err := r.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	r.CloseStreams()
	got := strings.Join(log.get(), ",")
	if got != "a.CloseStreams,d.CloseStreams" {
		t.Errorf("calls = %s", got)
	}
	var nilRegistry *Registry
	nilRegistry.CloseStreams()
}

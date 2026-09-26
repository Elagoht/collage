package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/Elagoht/collage/internal/types"
)

// ErrNilRegistry is returned by Register when called on a nil *Registry. Every
// other Registry method treats a nil receiver as an empty, inert registry (see the
// Registry doc comment); Register is the exception because there is no backing
// slice a nil receiver could append to.
var ErrNilRegistry = errors.New("collage: nil plugin registry")

// ErrNilPlugin is returned by Register when passed a nil Plugin.
var ErrNilPlugin = errors.New("collage: nil plugin")

// ErrEmptyPluginName is returned by Register when the plugin's Name method returns
// the empty string.
var ErrEmptyPluginName = errors.New("collage: empty plugin name")

// ErrDuplicatePlugin is returned by Register when a plugin with the same Name is
// already registered.
var ErrDuplicatePlugin = errors.New("collage: duplicate plugin")

// ErrRegistryStarted is returned by Register once Init has been called: plugins
// must be registered before startup, since Init runs each plugin's own Init in
// registration order and a plugin added afterward would never see it.
var ErrRegistryStarted = errors.New("collage: plugin registry already started")

// Registry owns plugin registration, lifecycle, and hook dispatch. The zero value
// is not ready to use; construct one with NewRegistry.
//
// A nil *Registry is a safe, inert registry: every method except Register treats it
// as having zero plugins, so the application can hold a *Registry unconditionally,
// whether or not the embedding user registers any plugin at all. Register is the
// one exception — it returns ErrNilRegistry, since there is no backing store a nil
// receiver could append a plugin to.
type Registry struct {
	mu      sync.RWMutex
	logger  *slog.Logger
	plugins []Plugin
	started bool
}

// NewRegistry returns an empty Registry that logs through logger. A nil logger
// falls back to slog.Default().
func NewRegistry(logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{logger: logger}
}

// Register adds p to the registry. It returns ErrNilRegistry when r is nil,
// ErrNilPlugin when p is nil, ErrEmptyPluginName when p.Name() is empty,
// ErrDuplicatePlugin when a plugin with the same name is already registered, and
// ErrRegistryStarted when Init has already been called.
func (r *Registry) Register(p Plugin) error {
	if r == nil {
		return ErrNilRegistry
	}
	if p == nil {
		return ErrNilPlugin
	}
	name := p.Name()
	if name == "" {
		return ErrEmptyPluginName
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.started {
		return ErrRegistryStarted
	}
	for _, existing := range r.plugins {
		if existing.Name() == name {
			return fmt.Errorf("%w: %q", ErrDuplicatePlugin, name)
		}
	}
	r.plugins = append(r.plugins, p)
	return nil
}

// Plugins returns a copy of the registered plugins, in registration order.
// Mutating the returned slice does not affect the registry. A nil Registry
// returns nil.
func (r *Registry) Plugins() []Plugin {
	return r.snapshot()
}

// Init calls Init on every registered plugin, in registration order, passing host
// to each. It marks the registry started, so any later Register call fails with
// ErrRegistryStarted, regardless of whether Init itself succeeds.
//
// If a plugin's Init fails — including by panicking — Init stops, calls Shutdown on
// the already-initialised plugins in reverse registration order (the failing
// plugin itself is not shut down, since it never finished initialising), and
// returns an error that wraps both the original failure and any error from that
// rollback via errors.Join. A nil Registry does nothing and returns nil.
func (r *Registry) Init(ctx context.Context, hostFor func(name string) Host) error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	r.started = true
	plugins := append([]Plugin(nil), r.plugins...)
	r.mu.Unlock()

	for i, p := range plugins {
		// One host per plugin, not one shared: a plugin's Config call has to find
		// its own section, and the section is chosen by the plugin's name.
		host := hostFor(p.Name())
		if err := safeCall(func() error { return p.Init(ctx, host) }); err != nil {
			initErr := fmt.Errorf("collage: plugin %q init: %w", p.Name(), err)

			var shutdownErr error
			for j := i - 1; j >= 0; j-- {
				if serr := safeCall(func() error { return plugins[j].Shutdown(ctx) }); serr != nil {
					shutdownErr = errors.Join(shutdownErr, fmt.Errorf("collage: plugin %q shutdown: %w", plugins[j].Name(), serr))
				}
			}
			return errors.Join(initErr, shutdownErr)
		}
	}
	return nil
}

// Shutdown calls Shutdown on every registered plugin, in reverse registration
// order. Unlike Init, it never stops early: every plugin is given a chance to shut
// down even if an earlier one — later in the reverse order — failed or panicked.
// Every failure is collected with errors.Join, so a caller can still inspect any
// one of them with errors.Is or errors.As. A nil Registry does nothing and returns
// nil.
func (r *Registry) Shutdown(ctx context.Context) error {
	plugins := r.snapshot()

	var err error
	for i := len(plugins) - 1; i >= 0; i-- {
		p := plugins[i]
		if serr := safeCall(func() error { return p.Shutdown(ctx) }); serr != nil {
			err = errors.Join(err, fmt.Errorf("collage: plugin %q shutdown: %w", p.Name(), serr))
		}
	}
	return err
}

// Request gives every plugin implementing RequestHook, in registration order, the
// request to shape, and returns it under the context they made and a function
// that hands each of them the final status, in the reverse order. A hook that
// panics is skipped, and the request is served under the context before it.
func (r *Registry) Request(req *http.Request) (*http.Request, func(status int)) {
	var finishers []func(int)
	for _, p := range r.snapshot() {
		hook, ok := p.(RequestHook)
		if !ok {
			continue
		}
		var ctx context.Context
		var finish func(int)
		if err := safeCall(func() error { ctx, finish = hook.OnRequest(req); return nil }); err != nil {
			r.logOrDefault().Error("collage: request hook panicked", "plugin", p.Name(), "err", err)
			continue
		}
		if ctx != nil {
			req = req.WithContext(ctx)
		}
		if finish != nil {
			finishers = append(finishers, finish)
		}
	}
	return req, func(status int) {
		for i := len(finishers) - 1; i >= 0; i-- {
			f := finishers[i]
			_ = safeCall(func() error { f(status); return nil })
		}
	}
}

// CloseStreams asks every plugin implementing StreamCloser to end its open
// streams, in registration order. A panic in one is contained, so the others
// still close theirs. A nil Registry does nothing.
func (r *Registry) CloseStreams() {
	for _, p := range r.snapshot() {
		if closer, ok := p.(StreamCloser); ok {
			_ = safeCall(func() error { closer.CloseStreams(); return nil })
		}
	}
}

// PageResolved dispatches ev to every registered plugin implementing
// PageResolvedHook, in registration order. It stops and returns a wrapped error at
// the first hook failure, including a contained panic. A nil Registry, or a
// registry with no plugin implementing the hook, is a no-op that returns nil.
func (r *Registry) PageResolved(ctx context.Context, ev *PageResolvedEvent) error {
	for _, p := range r.snapshot() {
		hook, ok := p.(PageResolvedHook)
		if !ok {
			continue
		}
		if err := runHook(p.Name(), "OnPageResolved", func() error { return hook.OnPageResolved(ctx, ev) }); err != nil {
			return err
		}
	}
	return nil
}

// BeforeRender dispatches ev to every registered plugin implementing
// BeforeRenderHook, in registration order. It stops and returns a wrapped error at
// the first hook failure, including a contained panic. A nil Registry, or a
// registry with no plugin implementing the hook, is a no-op that returns nil.
func (r *Registry) BeforeRender(ctx context.Context, ev *BeforeRenderEvent) error {
	for _, p := range r.snapshot() {
		hook, ok := p.(BeforeRenderHook)
		if !ok {
			continue
		}
		if err := runHook(p.Name(), "OnBeforeRender", func() error { return hook.OnBeforeRender(ctx, ev) }); err != nil {
			return err
		}
	}
	return nil
}

// AfterRender dispatches ev to every registered plugin implementing
// AfterRenderHook, in registration order. A plugin may replace ev.HTML; later
// plugins in the dispatch order see the replacement made by earlier ones, and the
// caller observes whatever ev.HTML holds once dispatch returns. It stops and
// returns a wrapped error at the first hook failure, including a contained panic.
// A nil Registry, or a registry with no plugin implementing the hook, is a no-op
// that returns nil.
func (r *Registry) AfterRender(ctx context.Context, ev *AfterRenderEvent) error {
	for _, p := range r.snapshot() {
		hook, ok := p.(AfterRenderHook)
		if !ok {
			continue
		}
		before := len(ev.Findings)
		if err := runHook(p.Name(), "OnAfterRender", func() error { return hook.OnAfterRender(ctx, ev) }); err != nil {
			return err
		}
		stamp(ev.Findings, before, p.Name())
	}
	return nil
}

// BuildFinished dispatches ev to every registered plugin implementing
// BuildFinishedHook, in registration order, stopping at the first that fails.
func (r *Registry) BuildFinished(ctx context.Context, ev *BuildFinishedEvent) error {
	for _, p := range r.snapshot() {
		hook, ok := p.(BuildFinishedHook)
		if !ok {
			continue
		}
		before := len(ev.Findings)
		if err := runHook(p.Name(), "OnBuildFinished", func() error { return hook.OnBuildFinished(ctx, ev) }); err != nil {
			return err
		}
		stamp(ev.Findings, before, p.Name())
	}
	return nil
}

// stamp names the plugin on the findings it just added, from index from on.
func stamp(findings []types.Finding, from int, plugin string) {
	for i := from; i < len(findings); i++ {
		if findings[i].Plugin == "" {
			findings[i].Plugin = plugin
		}
	}
}

// CacheWrite dispatches ev to every registered plugin implementing CacheWriteHook,
// in registration order. A plugin may set ev.Skip or adjust ev.TTL and ev.Tags;
// later plugins see the changes made by earlier ones. It stops and returns a
// wrapped error at the first hook failure, including a contained panic. A nil
// Registry, or a registry with no plugin implementing the hook, is a no-op that
// returns nil.
func (r *Registry) CacheWrite(ctx context.Context, ev *CacheWriteEvent) error {
	for _, p := range r.snapshot() {
		hook, ok := p.(CacheWriteHook)
		if !ok {
			continue
		}
		if err := runHook(p.Name(), "OnCacheWrite", func() error { return hook.OnCacheWrite(ctx, ev) }); err != nil {
			return err
		}
	}
	return nil
}

// CacheInvalidate dispatches ev to every registered plugin implementing
// CacheInvalidateHook, in registration order. It stops and returns a wrapped error
// at the first hook failure, including a contained panic. A nil Registry, or a
// registry with no plugin implementing the hook, is a no-op that returns nil.
func (r *Registry) CacheInvalidate(ctx context.Context, ev *CacheInvalidateEvent) error {
	for _, p := range r.snapshot() {
		hook, ok := p.(CacheInvalidateHook)
		if !ok {
			continue
		}
		if err := runHook(p.Name(), "OnCacheInvalidate", func() error { return hook.OnCacheInvalidate(ctx, ev) }); err != nil {
			return err
		}
	}
	return nil
}

// Error dispatches ev to every registered plugin implementing ErrorHook, in
// registration order. Unlike the other dispatch methods, a failure from one
// plugin's OnError — a returned error or a contained panic — is logged through
// r's logger and swallowed rather than returned: an error handler that itself
// errors must not recurse into another round of error handling, and dispatch
// continues so the remaining plugins still observe ev. Error therefore always
// returns nil. A nil Registry, or a registry with no plugin implementing the hook,
// is a no-op.
func (r *Registry) Error(ctx context.Context, ev *ErrorEvent) error {
	for _, p := range r.snapshot() {
		hook, ok := p.(ErrorHook)
		if !ok {
			continue
		}
		if err := safeCall(func() error { return hook.OnError(ctx, ev) }); err != nil {
			r.logOrDefault().Error("plugin OnError hook failed", "plugin", p.Name(), "error", err)
		}
	}
	return nil
}

// snapshot returns a defensive copy of r's registered plugins, in registration
// order, safe to range over and call into without holding r.mu. A nil r returns
// nil.
func (r *Registry) snapshot() []Plugin {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Plugin(nil), r.plugins...)
}

// logOrDefault returns r's logger, falling back to slog.Default() for a Registry
// constructed without NewRegistry. r is never nil here: every caller reaches this
// only from inside a loop over a non-empty snapshot, which is itself only non-empty
// for a non-nil r.
func (r *Registry) logOrDefault() *slog.Logger {
	if r.logger == nil {
		return slog.Default()
	}
	return r.logger
}

// runHook runs fn — one plugin's hook method — with panic safety via safeCall, and
// wraps any resulting error to name the plugin and the hook that produced it.
func runHook(pluginName, hookName string, fn func() error) error {
	if err := safeCall(fn); err != nil {
		return fmt.Errorf("collage: plugin %q %s: %w", pluginName, hookName, err)
	}
	return nil
}

// safeCall runs fn and converts a panic into an error, so a panicking plugin
// cannot take down the host process. It is a small, local twin of
// internal/render's Execute/PanicError: this package must not import
// internal/render, since that dependency would point the wrong way for a package
// the app layer composes on top of render, so the recover-to-error shape is
// duplicated here deliberately rather than shared.
func safeCall(fn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil { // any: a recovered panic value is any by the language definition
			err = fmt.Errorf("collage: plugin panicked: %v", recovered)
		}
	}()
	return fn()
}

// DocumentRendered dispatches ev to every registered plugin implementing
// DocumentRenderedHook, in registration order. A plugin may replace ev.Body; later
// plugins see what earlier ones produced. It stops and returns a wrapped error at
// the first hook failure, including a contained panic. A nil Registry, or one with
// no plugin implementing the hook, is a no-op that returns nil.
func (r *Registry) DocumentRendered(ctx context.Context, ev *DocumentRenderedEvent) error {
	for _, p := range r.snapshot() {
		hook, ok := p.(DocumentRenderedHook)
		if !ok {
			continue
		}
		if err := runHook(p.Name(), "OnDocumentRendered", func() error { return hook.OnDocumentRendered(ctx, ev) }); err != nil {
			return err
		}
	}
	return nil
}

package plugin

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

func TestRegistry_PageResolved(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	mustRegister(t, r, newHookPlugin("a", log))
	mustRegister(t, r, newHookPlugin("b", log))

	page := &types.Page{Name: "home"}
	ev := &PageResolvedEvent{Page: page, Locale: "en", Path: "/"}
	if err := r.PageResolved(context.Background(), ev); err != nil {
		t.Fatalf("PageResolved() error = %v, want nil", err)
	}
	want := []string{"OnPageResolved:a", "OnPageResolved:b"}
	if got := log.get(); !equalStrings(got, want) {
		t.Fatalf("PageResolved() call order = %v, want %v", got, want)
	}
}

func TestRegistry_BeforeRender(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	mustRegister(t, r, newHookPlugin("a", log))
	mustRegister(t, r, newHookPlugin("b", log))

	if err := r.BeforeRender(context.Background(), &BeforeRenderEvent{}); err != nil {
		t.Fatalf("BeforeRender() error = %v, want nil", err)
	}
	want := []string{"OnBeforeRender:a", "OnBeforeRender:b"}
	if got := log.get(); !equalStrings(got, want) {
		t.Fatalf("BeforeRender() call order = %v, want %v", got, want)
	}
}

func TestRegistry_AfterRender_HTMLReplacementObservedByCaller(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	first := newHookPlugin("first", log)
	first.afterRenderMutate = func(ev *AfterRenderEvent) {
		ev.HTML = []byte("<p>from first</p>")
	}
	second := newHookPlugin("second", log)
	second.afterRenderMutate = func(ev *AfterRenderEvent) {
		// second must see first's replacement, not the original.
		ev.HTML = append(ev.HTML, []byte("<p>from second</p>")...)
	}
	mustRegister(t, r, first)
	mustRegister(t, r, second)

	ev := &AfterRenderEvent{HTML: []byte("<p>original</p>")}
	if err := r.AfterRender(context.Background(), ev); err != nil {
		t.Fatalf("AfterRender() error = %v, want nil", err)
	}

	want := "<p>from first</p><p>from second</p>"
	if string(ev.HTML) != want {
		t.Fatalf("ev.HTML = %q, want %q", ev.HTML, want)
	}
}

func TestRegistry_AfterRender_PanicIsContainedAndNamed(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	ok := newHookPlugin("ok", log)
	panicky := newHookPlugin("panicky", log)
	panicky.afterRenderPanics = true
	mustRegister(t, r, ok)
	mustRegister(t, r, panicky)

	err := r.AfterRender(context.Background(), &AfterRenderEvent{})
	if err == nil {
		t.Fatal("AfterRender() error = nil, want non-nil for a panicking hook")
	}
	if !strings.Contains(err.Error(), "panicky") {
		t.Fatalf("AfterRender() error = %v, want it to name the panicking plugin", err)
	}
	if !strings.Contains(err.Error(), "OnAfterRender") {
		t.Fatalf("AfterRender() error = %v, want it to name the hook", err)
	}
}

func TestRegistry_CacheWrite_SkipHonoured(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	skipper := newHookPlugin("skipper", log)
	skipper.cacheWriteMutate = func(ev *CacheWriteEvent) {
		ev.Skip = true
	}
	mustRegister(t, r, skipper)

	ev := &CacheWriteEvent{Key: "k", TTL: 0, Tags: nil}
	if err := r.CacheWrite(context.Background(), ev); err != nil {
		t.Fatalf("CacheWrite() error = %v, want nil", err)
	}
	if !ev.Skip {
		t.Fatal("ev.Skip = false after a plugin set it true, want true")
	}
}

func TestRegistry_CacheWrite_AdjustsTTLAndTags(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	adjuster := newHookPlugin("adjuster", log)
	adjuster.cacheWriteMutate = func(ev *CacheWriteEvent) {
		ev.Tags = append(ev.Tags, "extra")
	}
	mustRegister(t, r, adjuster)

	ev := &CacheWriteEvent{Key: "k", Tags: []string{"base"}}
	if err := r.CacheWrite(context.Background(), ev); err != nil {
		t.Fatalf("CacheWrite() error = %v, want nil", err)
	}
	want := []string{"base", "extra"}
	if !equalStrings(ev.Tags, want) {
		t.Fatalf("ev.Tags = %v, want %v", ev.Tags, want)
	}
}

func TestRegistry_CacheInvalidate(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	mustRegister(t, r, newHookPlugin("a", log))

	ev := &CacheInvalidateEvent{Tags: []string{"post:1"}}
	if err := r.CacheInvalidate(context.Background(), ev); err != nil {
		t.Fatalf("CacheInvalidate() error = %v, want nil", err)
	}
	want := []string{"OnCacheInvalidate:a"}
	if got := log.get(); !equalStrings(got, want) {
		t.Fatalf("CacheInvalidate() call order = %v, want %v", got, want)
	}
}

func TestRegistry_Error_HookFailureIsLoggedAndSwallowed(t *testing.T) {
	log := &callLog{}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	r := NewRegistry(logger)

	failing := newHookPlugin("failing", log)
	failing.errorErr = errTestHook
	next := newHookPlugin("next", log)
	mustRegister(t, r, failing)
	mustRegister(t, r, next)

	err := r.Error(context.Background(), &ErrorEvent{Err: errors.New("collage: boom")})
	if err != nil {
		t.Fatalf("Error() error = %v, want nil (OnError failures are swallowed)", err)
	}

	// Both plugins must still have been called: one broken error hook must not
	// stop the others from observing the event.
	want := []string{"OnError:failing", "OnError:next"}
	if got := log.get(); !equalStrings(got, want) {
		t.Fatalf("Error() call order = %v, want %v", got, want)
	}

	if !strings.Contains(buf.String(), "failing") {
		t.Fatalf("log output = %q, want it to name the failing plugin", buf.String())
	}
}

func TestRegistry_Error_PanicIsLoggedAndSwallowed(t *testing.T) {
	log := &callLog{}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	r := NewRegistry(logger)

	panicky := newHookPlugin("panicky", log)
	panicky.errorPanics = true
	mustRegister(t, r, panicky)

	err := r.Error(context.Background(), &ErrorEvent{})
	if err != nil {
		t.Fatalf("Error() error = %v, want nil (a panicking OnError must not escalate)", err)
	}
	if !strings.Contains(buf.String(), "panicky") {
		t.Fatalf("log output = %q, want it to name the panicking plugin", buf.String())
	}
}

func TestRegistry_HookDispatch_StopsAtFirstFailure(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	failing := newHookPlugin("failing", log)
	failing.pageResolvedErr = errTestHook
	never := newHookPlugin("never", log)
	mustRegister(t, r, failing)
	mustRegister(t, r, never)

	err := r.PageResolved(context.Background(), &PageResolvedEvent{})
	if !errors.Is(err, errTestHook) {
		t.Fatalf("PageResolved() error = %v, want it to wrap errTestHook", err)
	}
	if !strings.Contains(err.Error(), "failing") {
		t.Fatalf("PageResolved() error = %v, want it to name the failing plugin", err)
	}
	for _, call := range log.get() {
		if strings.Contains(call, "never") {
			t.Fatalf("plugin after the failing one was still called: %q", call)
		}
	}
}

func TestRegistry_HookDispatch_NilAndEmptyRegistryAreNoOps(t *testing.T) {
	var nilRegistry *Registry
	empty := NewRegistry(nil)
	ctx := context.Background()

	for name, r := range map[string]*Registry{"nil": nilRegistry, "empty": empty} {
		t.Run(name, func(t *testing.T) {
			if err := r.PageResolved(ctx, &PageResolvedEvent{}); err != nil {
				t.Errorf("PageResolved() error = %v, want nil", err)
			}
			if err := r.BeforeRender(ctx, &BeforeRenderEvent{}); err != nil {
				t.Errorf("BeforeRender() error = %v, want nil", err)
			}
			if err := r.AfterRender(ctx, &AfterRenderEvent{}); err != nil {
				t.Errorf("AfterRender() error = %v, want nil", err)
			}
			if err := r.CacheWrite(ctx, &CacheWriteEvent{}); err != nil {
				t.Errorf("CacheWrite() error = %v, want nil", err)
			}
			if err := r.CacheInvalidate(ctx, &CacheInvalidateEvent{}); err != nil {
				t.Errorf("CacheInvalidate() error = %v, want nil", err)
			}
			if err := r.Error(ctx, &ErrorEvent{}); err != nil {
				t.Errorf("Error() error = %v, want nil", err)
			}
			if got := r.Plugins(); got != nil {
				t.Errorf("Plugins() = %v, want nil", got)
			}
		})
	}
}

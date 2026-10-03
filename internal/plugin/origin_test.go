package plugin

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestParseOrigin(t *testing.T) {
	good := map[string]string{
		"https://example.com":       "https://example.com",
		"https://example.com/":      "https://example.com",
		"http://localhost:3000":     "http://localhost:3000",
		"https://acme.app.com:8443": "https://acme.app.com:8443",
	}
	for raw, want := range good {
		got, err := ParseOrigin(raw)
		if err != nil || got != want {
			t.Errorf("ParseOrigin(%q) = %q, %v; want %q, nil", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"", "example.com", "ftp://example.com", "https://", "https://u:p@example.com",
		"https://example.com/blog", "https://example.com?x=1", "https://example.com#top", "mailto:a@b.c",
	} {
		if got, err := ParseOrigin(raw); !errors.Is(err, ErrInvalidOrigin) {
			t.Errorf("ParseOrigin(%q) = %q, %v; want ErrInvalidOrigin", raw, got, err)
		}
	}
}

type originPlugin struct {
	name   string
	origin string
	known  bool
	panics bool
	asked  atomic.Int32
}

func (p *originPlugin) Name() string                     { return p.name }
func (p *originPlugin) Version() string                  { return "0" }
func (p *originPlugin) Init(context.Context, Host) error { return nil }
func (p *originPlugin) Shutdown(context.Context) error   { return nil }
func (p *originPlugin) Origin(_ context.Context, _ string) (string, bool) {
	p.asked.Add(1)
	if p.panics {
		panic("boom")
	}
	return p.origin, p.known
}

type plainPlugin struct{ name string }

func (p *plainPlugin) Name() string                     { return p.name }
func (p *plainPlugin) Version() string                  { return "0" }
func (p *plainPlugin) Init(context.Context, Host) error { return nil }
func (p *plainPlugin) Shutdown(context.Context) error   { return nil }

func registryOf(t *testing.T, plugins ...Plugin) *Registry {
	t.Helper()
	r := NewRegistry(nil)
	for _, p := range plugins {
		if err := r.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestRegistryOrigin_FirstKnownWinsNormalized(t *testing.T) {
	unknown := &originPlugin{name: "a"}
	first := &originPlugin{name: "b", origin: "https://acme.example/", known: true}
	second := &originPlugin{name: "c", origin: "https://other.example", known: true}
	r := registryOf(t, &plainPlugin{name: "x"}, unknown, first, second)
	got, ok := r.Origin(context.Background(), "acme.test", nil)
	if !ok || got != "https://acme.example" {
		t.Errorf("Origin = %q, %v; want https://acme.example, true", got, ok)
	}
	if second.asked.Load() != 0 {
		t.Errorf("a resolver after the one that knew the host was asked")
	}
	if !r.HasOriginResolver() {
		t.Errorf("HasOriginResolver = false with resolvers registered")
	}
}

func TestRegistryOrigin_InvalidIsReportedAndSkipped(t *testing.T) {
	bad := &originPlugin{name: "bad", origin: "ftp://x", known: true}
	good := &originPlugin{name: "good", origin: "https://good.example", known: true}
	r := registryOf(t, bad, good)
	var reported []string
	got, ok := r.Origin(context.Background(), "h", func(plugin, origin string) { reported = append(reported, plugin+"="+origin) })
	if !ok || got != "https://good.example" {
		t.Errorf("Origin = %q, %v; want the next resolver's", got, ok)
	}
	if len(reported) != 1 || reported[0] != "bad=ftp://x" {
		t.Errorf("invalid reported %q, want [bad=ftp://x]", reported)
	}
}

func TestRegistryOrigin_PanicIsSkipped(t *testing.T) {
	r := registryOf(t, &originPlugin{name: "p", panics: true})
	if got, ok := r.Origin(context.Background(), "h", nil); ok || got != "" {
		t.Errorf("Origin = %q, %v; want \"\", false after a panic", got, ok)
	}
}

func TestRegistryOrigin_NoneAndNil(t *testing.T) {
	r := registryOf(t, &plainPlugin{name: "x"})
	if r.HasOriginResolver() {
		t.Errorf("HasOriginResolver = true without one")
	}
	if _, ok := r.Origin(context.Background(), "h", nil); ok {
		t.Errorf("Origin known without a resolver")
	}
	var nilRegistry *Registry
	if nilRegistry.HasOriginResolver() {
		t.Errorf("nil registry has a resolver")
	}
	if _, ok := nilRegistry.Origin(context.Background(), "h", nil); ok {
		t.Errorf("nil registry knew an origin")
	}
}

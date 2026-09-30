package collage_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// baseURLReader is a plugin that records what Host.BaseURL and ConfigHost.BaseURL
// reported to it, one in Init and one in Configure.
type baseURLReader struct {
	fromConfigure string
	fromInit      string
}

func (p *baseURLReader) Name() string                   { return "test/baseurl" }
func (p *baseURLReader) Version() string                { return "0" }
func (p *baseURLReader) Shutdown(context.Context) error { return nil }

func (p *baseURLReader) Configure(_ context.Context, host collage.ConfigHost) error {
	p.fromConfigure = host.BaseURL()
	return nil
}

func (p *baseURLReader) Init(_ context.Context, host collage.Host) error {
	p.fromInit = host.BaseURL()
	return nil
}

func validBaseConfig() *collage.Config {
	return &collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{Root: "templates"},
		Locale:   collage.LocaleConfig{Default: "en", Supported: []string{"en"}},
	}
}

// TestConfig_BaseURLValidation: a bare origin is accepted (empty included), and
// anything with a path, query, fragment, credentials, wrong scheme or no host is
// refused with ErrInvalidBaseURL.
func TestConfig_BaseURLValidation(t *testing.T) {
	good := []string{
		"", // unset is fine
		"https://example.com",
		"http://example.com",
		"https://example.com:8443",
		"https://example.com/", // a single trailing slash is allowed and trimmed
		"http://localhost:6060",
	}
	for _, v := range good {
		cfg := validBaseConfig()
		cfg.BaseURL = v
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(BaseURL=%q) = %v, want nil", v, err)
		}
	}

	bad := []string{
		"example.com",              // no scheme
		"//example.com",            // scheme-relative
		"ftp://example.com",        // wrong scheme
		"https://",                 // no host
		"https://example.com/path", // a path
		"https://example.com?q=1",  // a query
		"https://example.com#frag", // a fragment
		"https://user@example.com", // credentials
		"not a url",
	}
	for _, v := range bad {
		cfg := validBaseConfig()
		cfg.BaseURL = v
		if err := cfg.Validate(); !errors.Is(err, collage.ErrInvalidBaseURL) {
			t.Errorf("Validate(BaseURL=%q) = %v, want ErrInvalidBaseURL", v, err)
		}
	}
}

// TestBaseURL_ReachesAPluginNormalized: an application's BaseURL reaches a plugin
// through both Host.BaseURL (in Init) and ConfigHost.BaseURL (in Configure), with
// the trailing slash trimmed, so a plugin can join a path to it cleanly.
func TestBaseURL_ReachesAPluginNormalized(t *testing.T) {
	p := &baseURLReader{}
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte("<p>x</p>")}}, Root: "t"},
		Locale:   collage.LocaleConfig{Default: "en", Supported: []string{"en"}},
		BaseURL:  "https://example.com/",
		Plugins:  []collage.Plugin{p},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = app.Handler() // building the handler runs plugin Init

	const want = "https://example.com"
	if p.fromConfigure != want {
		t.Errorf("ConfigHost.BaseURL in Configure = %q, want %q", p.fromConfigure, want)
	}
	if p.fromInit != want {
		t.Errorf("Host.BaseURL in Init = %q, want %q", p.fromInit, want)
	}
}

// TestBaseURL_EmptyIsEmpty: with no BaseURL set, a plugin reads "" and takes its
// own, rather than a made-up default.
func TestBaseURL_EmptyIsEmpty(t *testing.T) {
	p := &baseURLReader{}
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte("<p>x</p>")}}, Root: "t"},
		Locale:   collage.LocaleConfig{Default: "en", Supported: []string{"en"}},
		Plugins:  []collage.Plugin{p},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = app.Handler()
	if p.fromInit != "" {
		t.Errorf("Host.BaseURL with none set = %q, want empty", p.fromInit)
	}
}

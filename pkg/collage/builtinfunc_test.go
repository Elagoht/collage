package collage_test

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// shadower is a plugin that registers a template function under a name.
type shadower struct {
	name string
	err  error
}

func (s *shadower) Name() string                             { return "test/shadower" }
func (s *shadower) Version() string                          { return "0" }
func (s *shadower) Init(context.Context, collage.Host) error { return nil }
func (s *shadower) Shutdown(context.Context) error           { return nil }

func (s *shadower) Configure(_ context.Context, host collage.ConfigHost) error {
	s.err = host.AddTemplateFunc(s.name, func() string { return "plugin" })
	return nil
}

// A plugin's function under a built-in's name would silently replace the
// built-in every template was written against; it is refused, as a render
// function under one already is.
func TestAddTemplateFunc_RefusesBuiltinNames(t *testing.T) {
	for _, name := range []string{"json", "add", "withQuery", "upper"} {
		p := &shadower{name: name}
		if _, err := collage.New(&collage.Config{
			Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
			Template: collage.TemplateConfig{FS: fstest.MapFS{"t/x.html": {Data: []byte(`x`)}}, Root: "t"},
			Plugins:  []collage.Plugin{p},
		}); err != nil && !errors.Is(err, collage.ErrDuplicateTemplateFunc) {
			t.Fatalf("New: %v", err)
		}
		if !errors.Is(p.err, collage.ErrDuplicateTemplateFunc) {
			t.Errorf("AddTemplateFunc(%q) = %v, want ErrDuplicateTemplateFunc", name, p.err)
		}
	}
}

// A function the render engine binds anew for every render is never the one
// Config.Template.Funcs gives under its name: New says so rather than leave an
// application's own withQuery silently unused.
func TestNew_WarnsWhenFuncsNameARenderBoundBuiltin(t *testing.T) {
	var logs lockedBuffer
	if _, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{
			FS:    fstest.MapFS{"t/x.html": {Data: []byte(`x`)}},
			Root:  "t",
			Funcs: template.FuncMap{"withQuery": func(string, int) string { return "mine" }, "add": func(a, b int) int { return a + b }},
		},
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}); err != nil {
		t.Fatal(err)
	}
	got := logs.String()
	if !strings.Contains(got, "withQuery") || !strings.Contains(got, "level=WARN") {
		t.Errorf("no warning naming withQuery:\n%s", got)
	}
	if strings.Contains(got, `"add"`) || strings.Contains(got, "func=add") {
		t.Errorf("add is not bound per render, yet was warned about:\n%s", got)
	}
}

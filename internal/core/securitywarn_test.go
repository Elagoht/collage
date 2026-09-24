package core

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// capturingLogs records what an application logged, so a test can assert on a
// warning without reading a terminal.
type capturingLogs struct {
	slog.Handler
	lines *strings.Builder
}

func newCapturingLogger() (*slog.Logger, *strings.Builder) {
	var b strings.Builder
	return slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelWarn})), &b
}

func postAction(name string) *types.Action {
	return &types.Action{
		Name:    name,
		Paths:   map[string]string{"en": "/" + name},
		Methods: []string{http.MethodPost},
		Handler: func(context.Context, *types.RenderContext) (*types.ActionResult, error) { return nil, nil },
	}
}

// An application with no form has nothing to verify a token with and nothing to
// lose by a generated key. Telling it anyway is how a warning becomes noise, and a
// warning that is usually noise is one nobody reads.
func TestGeneratedKey_NotWarnedAboutWithoutAForm(t *testing.T) {
	logger, logs := newCapturingLogger()
	app := newTestAppWith(t, defaultTemplates(), func(cfg *Config) { cfg.Logger = logger })
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}

	app.Handler() // closes registration

	if strings.Contains(logs.String(), "CSRFKey") {
		t.Errorf("an application with no form was warned about a key it has no use for:\n%s", logs.String())
	}
}

// One that does answer a form will refuse every submission made before its last
// restart, which is worth interrupting for.
func TestGeneratedKey_WarnedAboutWithAForm(t *testing.T) {
	logger, logs := newCapturingLogger()
	app := newTestAppWith(t, defaultTemplates(), func(cfg *Config) { cfg.Logger = logger })
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.RegisterAction(postAction("subscribe")); err != nil {
		t.Fatalf("RegisterAction: %v", err)
	}

	app.Handler()

	if !strings.Contains(logs.String(), "CSRFKey") {
		t.Errorf("an application with a form was not warned about its generated key:\n%s", logs.String())
	}
}

// And an application that set a key is told nothing, whatever it registered.
func TestGeneratedKey_NotWarnedAboutWhenOneWasSet(t *testing.T) {
	logger, logs := newCapturingLogger()
	app := newTestAppWith(t, defaultTemplates(), func(cfg *Config) {
		cfg.Logger = logger
		cfg.Security.CSRFKey = []byte("a key the application chose")
	})
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.RegisterAction(postAction("subscribe")); err != nil {
		t.Fatalf("RegisterAction: %v", err)
	}

	app.Handler()

	if strings.Contains(logs.String(), "CSRFKey") {
		t.Errorf("an application that set a key was warned about one anyway:\n%s", logs.String())
	}
}

// In development a generated key is the expected state rather than a mistake:
// nobody sets a production secret to try a form on their own machine, and a
// restart is what development consists of. So it is said, but not as a warning.
func TestGeneratedKey_NotWarnedAboutInDevelopment(t *testing.T) {
	var b strings.Builder
	logger := slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelInfo}))
	app := newTestAppWith(t, defaultTemplates(), func(cfg *Config) {
		cfg.Logger = logger
		cfg.DevMode = true
	})
	if err := app.RegisterPage(newHomePage()); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	if err := app.RegisterAction(postAction("subscribe")); err != nil {
		t.Fatalf("RegisterAction: %v", err)
	}

	app.Handler()

	for _, line := range strings.Split(b.String(), "\n") {
		if !strings.Contains(line, "CSRFKey") {
			continue
		}
		if !strings.Contains(line, "level=INFO") {
			t.Errorf("in development the generated key was logged as %q, want INFO", line)
		}
		return
	}
	t.Errorf("in development the generated key was not mentioned at all:\n%s", b.String())
}

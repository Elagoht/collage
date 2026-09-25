package collage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildTestApp registers the framework's minimal example page — a layout, a
// static home page — on a fresh App over templateRoot, and returns it.
func buildTestApp(t *testing.T) *App {
	t.Helper()

	app, err := New(&Config{
		Server:   ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{Root: templateRoot(t)},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	layout := NewFragment("layout", "layouts/default.html").
		WithSlot("content", true, false).
		Build()
	content := NewFragment("home-content", "pages/home.html").Build()
	page := NewPage("home").
		WithLayout(layout).
		WithContent(content).
		WithPath("en", "/").
		Static().
		Build()

	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	return app
}

// TestNewBuilder_ReachesTheRealBuilder proves NewBuilder is not a second,
// weaker implementation: it renders through the exact same App.RenderPath the
// HTTP handler uses, and its safety refusals (ErrNilRenderer,
// ErrInvalidOutDir here; the symlink-escape and dangerous-output-directory
// checks are internal/build's own and covered there) are reachable through
// the public alias.
func TestNewBuilder_ReachesTheRealBuilder(t *testing.T) {
	app := buildTestApp(t)
	outDir := t.TempDir()

	builder, err := NewBuilder(app, BuildOptions{OutDir: outDir})
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}

	report, err := builder.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.Written) != 1 {
		t.Fatalf("report.Written = %v, want exactly one file", report.Written)
	}

	body, err := os.ReadFile(filepath.Join(outDir, "index.html"))
	if err != nil {
		t.Fatalf("read %s: %v", report.Written[0], err)
	}
	if len(body) == 0 {
		t.Fatal("rendered file is empty")
	}
}

func TestNewBuilder_NilApp(t *testing.T) {
	if _, err := NewBuilder(nil, BuildOptions{OutDir: "dist"}); !errors.Is(err, ErrNilRenderer) {
		t.Fatalf("NewBuilder(nil, ...) error = %v, want ErrNilRenderer", err)
	}
}

func TestNewBuilder_EmptyOutDir(t *testing.T) {
	app := buildTestApp(t)
	if _, err := NewBuilder(app, BuildOptions{}); !errors.Is(err, ErrInvalidOutDir) {
		t.Fatalf("NewBuilder(..., {}) error = %v, want ErrInvalidOutDir", err)
	}
}

// TestNewBuilder_CleanRefusesRepositoryRoot exercises one of internal/build's
// own safety refusals through the public alias, so a regression there would
// also be visible to a caller who only ever imports pkg/collage.
func TestNewBuilder_CleanRefusesRepositoryRoot(t *testing.T) {
	app := buildTestApp(t)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	builder, err := NewBuilder(app, BuildOptions{OutDir: repo, Clean: true})
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}

	_, err = builder.Build(context.Background())
	if !errors.Is(err, ErrDangerousOutDir) {
		t.Fatalf("Build() error = %v, want ErrDangerousOutDir", err)
	}
}

// TestBuildOptions_FieldsAlignWithInternalBuild is a compile-time check more
// than a runtime one: it exercises every BuildOptions field through the
// public alias so a field renamed or removed in internal/build.Options fails
// this package's own build, not just internal/build's.
func TestBuildOptions_FieldsAlignWithInternalBuild(t *testing.T) {
	opts := BuildOptions{
		OutDir:      t.TempDir(),
		Locales:     []string{"en"},
		Clean:       false,
		Concurrency: 2,
	}
	app := buildTestApp(t)
	if _, err := NewBuilder(app, opts); err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
}

// TestBuildReport_FieldsAlignWithInternalBuild is the Report-side counterpart
// of TestBuildOptions_FieldsAlignWithInternalBuild: it reads every BuildReport
// field through the public alias so a field renamed or removed in
// internal/build.Report fails this package's own build.
func TestBuildReport_FieldsAlignWithInternalBuild(t *testing.T) {
	app := buildTestApp(t)
	builder, err := NewBuilder(app, BuildOptions{OutDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	report, err := builder.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var written []string = report.Written
	var skipped []SkipRecord = report.Skipped
	var buildErrs []error = report.Errors
	var duration time.Duration = report.Duration

	if len(written) != 1 {
		t.Errorf("Written = %v, want exactly one file", written)
	}
	if len(skipped) != 0 {
		t.Errorf("Skipped = %v, want none", skipped)
	}
	if len(buildErrs) != 0 {
		t.Errorf("Errors = %v, want none", buildErrs)
	}
	if duration <= 0 {
		t.Error("Duration = 0, want a positive elapsed time")
	}
}

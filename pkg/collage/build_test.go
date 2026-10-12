package collage

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/build"
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
		WithSlot("content", SlotRequired, SlotSingle).
		Build()
	content := NewFragment("home-content", "pages/home.html").Build()
	page := NewPage("home").
		WithLayouts(layout).
		WithContent(content).
		WithPath("en", "/").
		Static().
		Build()

	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	return app
}

// TestBuild_ReachesTheRealBuilder proves Build is not a second, weaker
// implementation: it renders through the exact same App.RenderPath the HTTP
// handler uses, and its safety refusals (ErrNilApp, ErrEmptyOutDir here; the
// symlink-escape and dangerous-output-directory checks are internal/build's own
// and covered there) are reachable through pkg/collage.
func TestBuild_ReachesTheRealBuilder(t *testing.T) {
	app := buildTestApp(t)
	outDir := t.TempDir()

	report, err := Build(context.Background(), app, BuildOptions{OutDir: outDir})
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

func TestBuild_NilApp(t *testing.T) {
	if _, err := Build(context.Background(), nil, BuildOptions{OutDir: "dist"}); !errors.Is(err, ErrNilApp) {
		t.Fatalf("Build(nil app) error = %v, want ErrNilApp", err)
	}
	// An App New did not return has no application inside: the same refusal,
	// not a panic deep in the build.
	if _, err := Build(context.Background(), &App{}, BuildOptions{OutDir: "dist"}); !errors.Is(err, ErrNilApp) {
		t.Fatalf("Build(&App{}) error = %v, want ErrNilApp", err)
	}
}

func TestBuild_EmptyOutDir(t *testing.T) {
	app := buildTestApp(t)
	if _, err := Build(context.Background(), app, BuildOptions{}); !errors.Is(err, ErrEmptyOutDir) {
		t.Fatalf("Build(..., {}) error = %v, want ErrEmptyOutDir", err)
	}
}

// TestBuild_CleanRefusesRepositoryRoot exercises one of internal/build's own
// safety refusals through pkg/collage, so a regression there would also be
// visible to a caller who only ever imports pkg/collage.
func TestBuild_CleanRefusesRepositoryRoot(t *testing.T) {
	app := buildTestApp(t)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	_, err := Build(context.Background(), app, BuildOptions{OutDir: repo, Clean: true})
	if !errors.Is(err, ErrDangerousOutDir) {
		t.Fatalf("Build() error = %v, want ErrDangerousOutDir", err)
	}
}

// TestBuildOptions_FieldsAlignWithInternalBuild checks that every BuildOptions
// field reaches the build engine: BuildOptions.internal copies them one by one,
// and a field it forgot would read back as the zero value here. That no field
// exists on one side only is TestBuildTypes_MirrorTheEngine's job; a field
// left zero in the fixture below fails, so a new one has to be set here too.
func TestBuildOptions_FieldsAlignWithInternalBuild(t *testing.T) {
	opts := BuildOptions{
		OutDir:        t.TempDir(),
		Locales:       []string{"en"},
		Clean:         true,
		Concurrency:   2,
		AllowDegraded: true,
	}
	got := opts.internal()
	pub, eng := reflect.ValueOf(opts), reflect.ValueOf(got)
	for i := range pub.NumField() {
		name := pub.Type().Field(i).Name
		if pub.Field(i).IsZero() {
			t.Fatalf("BuildOptions.%s is zero in the fixture: set it so internal() is checked for it", name)
		}
		if e := eng.FieldByName(name); !e.IsValid() || !reflect.DeepEqual(e.Interface(), pub.Field(i).Interface()) {
			t.Fatalf("internal().%s = %v, want %v", name, e, pub.Field(i))
		}
	}
	app := buildTestApp(t)
	if _, err := Build(context.Background(), app, opts); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

// TestBuildReport_FieldsAlignWithInternalBuild reads every BuildReport field
// back from a real build, typed as this package declares it.
func TestBuildReport_FieldsAlignWithInternalBuild(t *testing.T) {
	app := buildTestApp(t)
	report, err := Build(context.Background(), app, BuildOptions{OutDir: t.TempDir()})
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

// A static build renders without a request, so it has no reader to ask a guard
// about: a guarded page written to disk is served by the static host to anyone.
// It is skipped and reported, never written.
func TestBuild_SkipsGuardedPages(t *testing.T) {
	app, err := New(&Config{
		Server:   ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{Root: templateRoot(t)},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	private := NewFragment("private", "layouts/default.html").
		WithGuard(func(ctx context.Context, r *http.Request) (*GuardDecision, error) {
			return &GuardDecision{Status: http.StatusSeeOther, Location: "/login"}, nil
		}).
		Build()
	page := NewPage("dashboard").WithLayouts(private).
		WithContent(NewFragment("home-content", "pages/home.html").Build()).
		WithPath("en", "/dashboard").Static().Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatalf("RegisterPage: %v", err)
	}
	outDir := t.TempDir()
	report, err := Build(context.Background(), app, BuildOptions{OutDir: outDir})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "dashboard", "index.html")); !os.IsNotExist(err) {
		t.Fatalf("guarded page was written to the build output (stat err = %v)", err)
	}
	found := false
	for _, skip := range report.Skipped {
		if skip.Name == "dashboard" && errors.Is(skip.Err, ErrGuarded) {
			found = true
		}
	}
	if !found {
		t.Fatalf("Skipped = %+v, want dashboard with ErrGuarded", report.Skipped)
	}
}

// TestBuildTypes_MirrorTheEngine pins the public build types to the engine's,
// field name by field name and in order. They are defined types copied by
// BuildOptions.internal and reportFrom, so a field added to either side and not
// to the other would otherwise be dropped without a word.
func TestBuildTypes_MirrorTheEngine(t *testing.T) {
	for _, pair := range []struct{ public, engine reflect.Type }{
		{reflect.TypeFor[BuildOptions](), reflect.TypeFor[build.Options]()},
		{reflect.TypeFor[BuildReport](), reflect.TypeFor[build.Report]()},
		{reflect.TypeFor[SkipRecord](), reflect.TypeFor[build.SkipRecord]()},
		{reflect.TypeFor[WarningRecord](), reflect.TypeFor[build.WarningRecord]()},
	} {
		if got, want := exportedFields(pair.public), exportedFields(pair.engine); !slices.Equal(got, want) {
			t.Errorf("%s fields = %v, engine's %s has %v; copy the field in both converters",
				pair.public.Name(), got, pair.engine.Name(), want)
		}
	}
}

func exportedFields(typ reflect.Type) []string {
	var names []string
	for f := range typ.Fields() {
		if f.IsExported() {
			names = append(names, f.Name)
		}
	}
	return names
}

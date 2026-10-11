package collage_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// finishWatcher records whether a build reached its finished hook.
type finishWatcher struct{ called bool }

func (*finishWatcher) Name() string                             { return "test/finish-watcher" }
func (*finishWatcher) Version() string                          { return "0.0.0" }
func (*finishWatcher) Init(context.Context, collage.Host) error { return nil }
func (*finishWatcher) Shutdown(context.Context) error           { return nil }

func (w *finishWatcher) OnBuildFinished(context.Context, *collage.BuildFinishedEvent) error {
	w.called = true
	return nil
}

// An output collision stops the build before OutDir is touched: a Clean build
// leaves the old output where it was, writes no 404 page or mounted file, and
// never reaches BuildFinished.
func TestBuild_OutputCollisionTouchesNothing(t *testing.T) {
	watcher := &finishWatcher{}
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<html><body><h1>p</h1></body></html>`)}}, Root: "t"},
		Plugins:  []collage.Plugin{watcher},
	})
	if err != nil {
		t.Fatal(err)
	}
	blog := collage.NewPage("blog").WithContent(collage.NewFragment("blog", "p.html").Build()).
		WithPath("en", "/blog").Build()
	if err := app.RegisterPage(blog); err != nil {
		t.Fatal(err)
	}
	notFound := collage.NewPage("missing").WithContent(collage.NewFragment("missing", "p.html").Build()).Build()
	if err := app.RegisterNotFoundPage(notFound); err != nil {
		t.Fatal(err)
	}
	doc := collage.NewDocument("blog-index", "text/html").WithPath("en", "/blog/index.html").
		WithBody([]byte("<p>doc</p>")).Build()
	if err := app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	if err := app.Mount("/static/", fstest.MapFS{"app.css": {Data: []byte("body{}")}}); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	old := filepath.Join(outDir, "old.txt")
	if err := os.WriteFile(old, []byte("previous build"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := collage.Build(context.Background(), app, collage.BuildOptions{OutDir: outDir, Clean: true})
	if !errors.Is(err, collage.ErrOutputPathCollision) {
		t.Fatalf("Build = %v, want ErrOutputPathCollision", err)
	}
	if report == nil || len(report.Errors) != 1 || len(report.Written) != 0 {
		t.Fatalf("report = %+v, want exactly the collision and nothing written", report)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "old.txt" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("OutDir holds %v, want only old.txt", names)
	}
	if watcher.called {
		t.Fatal("BuildFinished ran for a build that stopped on a collision")
	}
}

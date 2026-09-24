package collage_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// A directory named in DevWatch reloads a development page when a file in it
// changes — content read from disk, which is neither a template nor a mount.
func TestDevWatch_ReloadsOnAWatchedDirectory(t *testing.T) {
	content := t.TempDir()
	page := filepath.Join(content, "intro.md")
	if err := os.WriteFile(page, []byte("# One"), 0o644); err != nil {
		t.Fatal(err)
	}

	app, err := collage.New(&collage.Config{
		DevMode:  true,
		DevWatch: []string{content, filepath.Join(content, "missing")},
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>x</p>`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.RegisterPage(collage.NewPage("p").WithContent(collage.NewFragment("p", "p.html").Build()).
		WithPath("en", "/").Dynamic().Build()); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/_collage/reload", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	lines := bufio.NewScanner(res.Body)
	for lines.Scan() && lines.Text() != "event: hello" {
	}
	if err := os.WriteFile(page, []byte("# One, edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	for lines.Scan() {
		if lines.Text() == "event: reload" {
			return
		}
	}
	t.Fatal("no reload after a file in a watched directory changed")
}

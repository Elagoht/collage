package collage_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// readPlugin reads files through Host.OpenMounted from Init, the way a plugin
// reads a source image the site serves.
type readPlugin struct {
	paths []string
	got   map[string]string
	errs  map[string]error
}

func (*readPlugin) Name() string                   { return "test/read" }
func (*readPlugin) Version() string                { return "0" }
func (*readPlugin) Shutdown(context.Context) error { return nil }
func (p *readPlugin) Init(_ context.Context, host collage.Host) error {
	p.got, p.errs = map[string]string{}, map[string]error{}
	for _, path := range p.paths {
		f, err := host.OpenMounted(path)
		if err != nil {
			p.errs[path] = err
			continue
		}
		b, err := io.ReadAll(f)
		f.Close()
		p.got[path], p.errs[path] = string(b), err
	}
	return nil
}

func TestHost_OpenMountedReadsTheSitesOwnFiles(t *testing.T) {
	p := &readPlugin{paths: []string{"/static/icon.png", "/static/img/a.png", "/static/missing.png", "/other/icon.png",
		"/static/../secret", "/static/.env", "/static/"}}
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/x.html": {Data: []byte("x")}}, Root: "t"},
		Plugins:  []collage.Plugin{p},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{"icon.png": {Data: []byte("png")}, "img/a.png": {Data: []byte("a")}, ".env": {Data: []byte("SECRET=1")}}
	if err := app.Mount("/static/", files); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}

	if p.got["/static/icon.png"] != "png" || p.got["/static/img/a.png"] != "a" {
		t.Errorf("read %q, want the mounted files", p.got)
	}
	// Only what a request could reach: no file, no mount, a step out, a hidden
	// file or the bare prefix are each fs.ErrNotExist.
	for _, path := range []string{"/static/missing.png", "/other/icon.png", "/static/../secret", "/static/.env", "/static/"} {
		if !errors.Is(p.errs[path], fs.ErrNotExist) {
			t.Errorf("%s: err = %v, want fs.ErrNotExist", path, p.errs[path])
		}
	}
}

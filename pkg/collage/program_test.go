package collage

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// shutdownPlugin records whether its Shutdown ran, which Main owes a build and a
// command.
type shutdownPlugin struct{ commandPlugin }

func (p *shutdownPlugin) Shutdown(ctx context.Context) error {
	p.args = append(p.args, "shutdown")
	return nil
}

func TestRunMain_RunsACommandAndShutsDown(t *testing.T) {
	p := &shutdownPlugin{commandPlugin{name: "hello"}}
	var got Env
	t.Setenv("COLLAGE_DEV", "1")
	t.Setenv("HOST", "0.0.0.0")
	t.Setenv("PORT", "7070")
	newApp := func(env Env) (*App, error) {
		got = env
		app := registrationTestApp(t)
		return app, app.RegisterPlugin(p)
	}

	code := runMain(newApp, []string{"hello", "a"}, &bytes.Buffer{}, &bytes.Buffer{})

	if code != 0 || !p.ran {
		t.Fatalf("code = %d, ran = %v; want 0 and the command run", code, p.ran)
	}
	if strings.Join(p.args, " ") != "a shutdown" {
		t.Errorf("args then shutdown = %q, want %q", p.args, "a shutdown")
	}
	if want := (Env{Dev: true, Host: "0.0.0.0", Port: 7070, CacheDir: ".cache"}); got != want {
		t.Errorf("env = %+v, want %+v", got, want)
	}
}

func TestRunMain_PortFlagWinsOverTheEnvironment(t *testing.T) {
	t.Setenv("PORT", "7070")
	var got Env
	newApp := func(env Env) (*App, error) { got = env; return registrationTestApp(t), nil }

	runMain(newApp, []string{"-port", "8080", "collage-check"}, &bytes.Buffer{}, &bytes.Buffer{})

	if got.Port != 8080 || got.Host != "localhost" || got.Dev {
		t.Errorf("env = %+v, want port 8080 on localhost, not in development", got)
	}
}

func TestRunMain_UnknownCommandIsAUsageError(t *testing.T) {
	var stderr bytes.Buffer
	code := runMain(func(Env) (*App, error) { return registrationTestApp(t), nil }, []string{"nope"}, &bytes.Buffer{}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "nope") {
		t.Errorf("code = %d, stderr %q; want 2 naming the command", code, stderr.String())
	}
}

func TestRunMain_NewAppErrorExits1(t *testing.T) {
	var stderr bytes.Buffer
	code := runMain(func(Env) (*App, error) { return nil, errors.New("no cms") }, nil, &bytes.Buffer{}, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "no cms") {
		t.Errorf("code = %d, stderr %q; want 1 with the error", code, stderr.String())
	}
}

func TestRunMain_BuildsStaticFiles(t *testing.T) {
	out := t.TempDir()
	newApp := func(Env) (*App, error) {
		app := registrationTestApp(t)
		return app, app.RegisterPage(simplePage("home").WithPath("en", "/").Build())
	}
	var stdout bytes.Buffer

	code := runMain(newApp, []string{"-collage-build", "-out", out}, &stdout, &bytes.Buffer{})

	if code != 0 {
		t.Fatalf("code = %d, want 0; report:\n%s", code, stdout.String())
	}
	if html, err := os.ReadFile(filepath.Join(out, "index.html")); err != nil || !strings.Contains(string(html), "<h1>Home</h1>") {
		t.Errorf("index.html = %q, %v; want the home page", html, err)
	}
}

func TestEnv_PluginConfigPrefersEmbeddedOutsideDevelopment(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(pluginConfigFile, []byte(`{"disk": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{pluginConfigFile: {Data: []byte(`{"embedded": {}}`)}}

	for _, c := range []struct {
		env   Env
		files fs.FS
		want  string
	}{
		{Env{}, files, "embedded"},
		{Env{Dev: true}, files, "disk"},
		{Env{}, fstest.MapFS{}, "disk"},
		{Env{}, nil, "disk"},
	} {
		config, err := c.env.PluginConfig(c.files)
		if _, ok := config[c.want]; err != nil || !ok {
			t.Errorf("Dev %v, files %v: config = %v, %v; want %q's", c.env.Dev, c.files, config, err, c.want)
		}
	}

	if _, err := (Env{}).PluginConfig(fstest.MapFS{pluginConfigFile: {Data: []byte(`{`)}}); err == nil {
		t.Error("a malformed embedded file was accepted")
	}
}

func TestEnv_StaticPrefersDiskInDevelopment(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("static", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("static/app.css", []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{"static/app.css": {Data: []byte("embedded")}}

	for _, c := range []struct {
		env  Env
		want string
	}{{Env{}, "embedded"}, {Env{Dev: true}, "disk"}} {
		got, err := fs.ReadFile(c.env.Static(files, "static"), "app.css")
		if err != nil || string(got) != c.want {
			t.Errorf("Dev %v: app.css = %q, %v; want %q", c.env.Dev, got, err, c.want)
		}
	}

	if _, err := fs.ReadFile((Env{}).Static(nil, "static"), "app.css"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("no files: err = %v, want fs.ErrNotExist", err)
	}
	if _, err := fs.ReadFile((Env{}).Static(files, "../x"), "app.css"); err == nil {
		t.Error("an invalid directory served a file")
	}
}

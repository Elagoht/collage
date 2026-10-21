package cli

import (
	"context"
	"io"
	"strings"
	"testing"
)

// printingRunner answers every command by writing out to stdout.
type printingRunner struct {
	out  string
	args []string
}

func (p *printingRunner) Run(_ context.Context, _ string, _ []string, stdout, _ io.Writer, _ string, args ...string) error {
	p.args = args
	_, err := io.WriteString(stdout, p.out)
	return err
}

const routesFixture = `{
  "version": 1,
  "defaultLocale": "en",
  "locales": ["en"],
  "pages": [
    {"name": "home", "paths": {"en": "/"}, "strategy": "static", "layouts": ["layout"], "content": "home"},
    {"name": "users", "paths": {"en": "/admin/users"}, "strategy": "dynamic",
     "guards": ["adminGuard"], "content": "users",
     "fragmentPaths": [{"fragment": "table", "locale": "en", "pattern": "/admin/users/table"}]}
  ],
  "documents": [{"name": "feed", "paths": {"en": "/feed.xml"}, "contentType": "application/xml"}],
  "actions": [
    {"name": "rename", "paths": {"en": "/admin/users"}, "methods": ["POST"], "guarded": true},
    {"name": "ping", "paths": {"en": "/api/ping"}, "methods": ["POST", "PUT"]},
    {"name": "fire", "paths": {"en": "/api/fire"}, "methods": ["POST"], "guarded": true}
  ],
  "mounts": [{"prefix": "/static/", "files": ["/static/app.css"]}]
}`

func TestRoutes_ListsEveryRouteWithItsGuards(t *testing.T) {
	c, out, errOut := testCLI()
	runner := &printingRunner{out: routesFixture}
	c.Runner = runner
	if code := c.Run(context.Background(), []string{"routes"}); code != 0 {
		t.Fatalf("routes = %d; %s", code, errOut.String())
	}
	if got := strings.Join(runner.args, " "); got != "run . collage-inspect" {
		t.Errorf("ran go %s, want go run . collage-inspect", got)
	}
	lines := strings.Split(out.String(), "\n")
	want := [][]string{
		{"METHOD", "PATH", "KIND", "NAME", "NOTES"},
		{"GET", "/", "page", "home", "static"},
		{"GET", "/admin/users", "page", "users", "dynamic, guard adminGuard"},
		{"POST", "/admin/users", "action", "rename", "on page users, guard adminGuard, own guard"},
		{"GET", "/admin/users/table", "fragment", "table", "of page users, guard adminGuard"},
		{"POST", "/api/fire", "action", "fire", "own URL, own guard"},
		{"POST,PUT", "/api/ping", "action", "ping", "own URL, no guard"},
		{"GET", "/feed.xml", "document", "feed", "application/xml"},
		{"GET", "/static/*", "mount", "1 file"},
	}
	for i, fields := range want {
		if got := strings.Fields(lines[i]); strings.Join(got, " ") != strings.Join(fields, " ") {
			t.Errorf("line %d = %q, want %q", i, lines[i], strings.Join(fields, " "))
		}
	}
	if !strings.Contains(out.String(), "Not listed: handlers mounted with app.Handle") {
		t.Errorf("no word on what it cannot see:\n%s", out.String())
	}
}

func TestRoutes_ALocaleColumnOnlyWithLocales(t *testing.T) {
	c, out, _ := testCLI()
	c.Runner = &printingRunner{out: strings.Replace(
		strings.Replace(routesFixture, `"locales": ["en"]`, `"locales": ["en", "tr"]`, 1),
		`"paths": {"en": "/"}`, `"paths": {"en": "/", "tr": "/tr"}`, 1)}
	if code := c.Run(context.Background(), []string{"routes"}); code != 0 {
		t.Fatal(code)
	}
	if strings.Join(strings.Fields(strings.SplitN(out.String(), "\n", 2)[0]), " ") != "METHOD PATH LOCALE KIND NAME NOTES" {
		t.Errorf("no locale column:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "/tr") {
		t.Errorf("the Turkish path is missing:\n%s", out.String())
	}
}

func TestRoutes_RefusesWhatItCannotRead(t *testing.T) {
	for name, tt := range map[string]struct{ out, want string }{
		"another format": {strings.Replace(routesFixture, `"version": 1`, `"version": 2`, 1), "format 2; this CLI reads 1"},
		"not JSON":       {"collage: loaded .env\n", "printed no description"},
	} {
		t.Run(name, func(t *testing.T) {
			c, _, errOut := testCLI()
			c.Runner = &printingRunner{out: tt.out}
			if code := c.Run(context.Background(), []string{"routes"}); code != 1 {
				t.Errorf("routes = %d, want 1", code)
			}
			if !strings.Contains(errOut.String(), tt.want) {
				t.Errorf("stderr = %q, want %q", errOut.String(), tt.want)
			}
		})
	}
	c, _, _ := testCLI()
	if code := c.Run(context.Background(), []string{"routes", "extra"}); code != 2 {
		t.Errorf("routes extra = %d, want 2", code)
	}
}

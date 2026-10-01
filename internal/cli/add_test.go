package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeFiles writes files, by path relative to dir, into dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// readFile returns the file at name, relative to dir.
func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// add runs "collage add" in dir and returns its exit code, stdout and stderr.
func add(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	c, out, errOut := testCLI()
	code := c.Run(context.Background(), append(append([]string{"add"}, args...), "--dir", dir))
	return code, out.String(), errOut.String()
}

// wantInOrder fails unless each of parts appears in s, in that order.
func wantInOrder(t *testing.T, s string, parts ...string) {
	t.Helper()
	at := 0
	for _, part := range parts {
		i := strings.Index(s[at:], part)
		if i < 0 {
			t.Fatalf("%q is missing, or out of order, in:\n%s", part, s)
		}
		at += i + len(part)
	}
}

// Every kind added to a fresh demo project builds, and starts: the project's own
// tests build newApp, which registers everything, so a refusal at registration —
// a locale, a duplicate — fails here as it would on a server.
func TestAdd_DemoProjectBuildsAndStarts(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go binary not available")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	c, _, errOut := testCLI()
	target := filepath.Join(t.TempDir(), "proj")
	if code := c.Run(context.Background(), []string{"new", "demo", "--template", "demo", "-dir", target, "-module", "collageaddtest"}); code != 0 {
		t.Fatalf("new = %d; %s", code, errOut.String())
	}
	for _, args := range [][]string{
		{"page", "blog/post"},
		{"page", "blog/archive", "--file", "--path", "/arşiv"},
		{"page", "about"},
		{"fragment", "blog/sidebar"},
		{"action", "blog/comment"},
		{"action", "blog/ping", "--path", "/api/ping"},
		{"document", "feed", "--path", "/feed.xml", "--type", "application/xml"},
	} {
		if code, _, stderr := add(t, target, args...); code != 0 {
			t.Fatalf("add %v = %d; %s", args, code, stderr)
		}
	}

	routes := readFile(t, target, "routes.go")
	wantInOrder(t, routes,
		`blogpages "collageaddtest/pages/blog"`, `demopages "collageaddtest/pages/demo"`,
		`demopages.Hello(),`, `blogpages.Post(),`, `blogpages.Archive(),`, `pages.About(),`,
		`documents.Health(),`, `documents.Feed(),`,
		`// this one has a URL of its own.`, `actions.Count(),`, `actions.Ping(),`)
	if strings.Contains(routes, "actions.Comment()") {
		t.Error("an action with no path was registered; it is attached to a page instead")
	}
	if _, err := os.Stat(filepath.Join(target, "templates", "pages", "blog", "archive.html")); err != nil {
		t.Errorf("--file wrote no template: %v", err)
	}

	writeFiles(t, target, map[string]string{"added_test.go": `package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestAdded(t *testing.T) {
	c := client(t)
	for target, want := range map[string]string{"/blog/post": "<h1>Post</h1>", "/ar%C5%9Fiv": "<h1>Archive</h1>", "/about": "<h1>About</h1>"} {
		if res := c.Get(target).WantStatus(http.StatusOK); !strings.Contains(res.Body, want) {
			t.Errorf("%s: %s", target, res.Body)
		}
	}
	if res := c.Get("/feed.xml").WantStatus(http.StatusOK); res.Header.Get("Content-Type") != "application/xml" {
		t.Errorf("feed type = %q", res.Header.Get("Content-Type"))
	}
	req := c.Request(http.MethodPost, "/api/ping", nil)
	req.Header.Set("X-CSRF-Token", c.Get("/features").CSRFToken())
	c.Do(req).WantStatus(http.StatusNoContent)
}
`})

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(goBin, args...)
		cmd.Dir = target
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("mod", "edit", "-replace", "github.com/Elagoht/collage="+repoRoot)
	run("mod", "tidy")
	run("vet", "./...")
	run("test", "./...")

	cmd := exec.Command(filepath.Join(filepath.Dir(goBin), "gofmt"), "-l", ".")
	cmd.Dir = target
	if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
		t.Errorf("gofmt -l: %v\n%s", err, out)
	}
}

// sdyRoutes is a routes.go in the shape of a real project's: pages in a
// []*collage.Page literal with comments inside it, a page constructor called
// Register, and an action registered on its own.
const sdyRoutes = `package main

import (
	"fmt"

	"sdy/actions"
	"sdy/data/users"
	authpages "sdy/pages/auth"
	panelpages "sdy/pages/panel"

	"github.com/Elagoht/collage/pkg/collage"
)

// Registers every page, document and action to app.
func register(
	app *collage.App,
	userService *users.UserService,
) error {
	notFound := panelpages.NotFound()
	for _, page := range []*collage.Page{
		// Register all Pages with their needs
		authpages.Register(userService),
		panelpages.Home(userService), // the panel
		notFound,
	} {
		if err := app.RegisterPage(page); err != nil {
			return fmt.Errorf("register page %q: %w", page.Name, err)
		}
	}
	if err := app.RegisterAction(
		actions.Logout(userService),
	); err != nil {
		return err
	}
	return nil
}
`

// sdyProject returns a project with sdyRoutes, a Turkish default locale, a
// layout and a page, on disk.
func sdyProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":    "module sdy\n\ngo 1.26\n",
		"routes.go": sdyRoutes,
		"main.go": `package main

import "github.com/Elagoht/collage/pkg/collage"

var cfg = collage.Config{
	Locale:   collage.LocaleConfig{Default: "tr"},
	Template: collage.TemplateConfig{Root: "views", Extension: ".tmpl"},
}
`,
		"fragments/layouts/main.go": `package layouts

import "github.com/Elagoht/collage/pkg/collage"

func Auth(service string) *collage.Fragment { return nil }

func Master() *collage.Fragment { return nil }
`,
		"pages/auth/login.go": `package pages

import "github.com/Elagoht/collage/pkg/collage"

func Login() *collage.Page { return collage.NewPage("login").Build() }
`,
		"actions/auth.go": `package actions

import (
	"net/http"

	"sdy/actions/funcs"

	"github.com/Elagoht/collage/pkg/collage"
)

func Logout() *collage.Action {
	return collage.NewAction("logout").WithMethods(http.MethodPost).WithHandler(funcs.Logout()).Build()
}
`,
		"actions/funcs/auth.go": `package funcs

import (
	"context"

	"github.com/Elagoht/collage/pkg/collage"
)

func Logout() collage.ActionHandlerFunc { return nil }
`,
	})
	return dir
}

func TestAdd_PageIntoALiteralKeepsItsComments(t *testing.T) {
	dir := sdyProject(t)
	code, out, stderr := add(t, dir, "page", "panel/settings")
	if code != 0 {
		t.Fatalf("add = %d; %s", code, stderr)
	}

	routes := readFile(t, dir, "routes.go")
	wantInOrder(t, routes,
		"// Register all Pages with their needs",
		"panelpages.Home(userService), // the panel",
		"notFound,",
		"panelpages.Settings(),",
		"} {")
	if strings.Count(routes, `panelpages "sdy/pages/panel"`) != 1 {
		t.Errorf("the panel pages were imported again:\n%s", routes)
	}
	page := readFile(t, dir, "pages/panel/settings.go")
	for _, want := range []string{`WithPath("tr", "/panel/settings")`, "layouts.Master()", `fragments "sdy/fragments/pages/panel"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s:\n%s", want, page)
		}
	}
	if !strings.Contains(out, "locale tr, from main.go") {
		t.Errorf("stdout does not say where the locale came from:\n%s", out)
	}
}

// A page constructor called Register is not the application's Register: an
// action or a document is never put into its arguments, and the command says
// what to register by hand instead.
func TestAdd_ARegisterPageIsNotTheAppsRegister(t *testing.T) {
	dir := sdyProject(t)
	before := readFile(t, dir, "routes.go")
	for _, args := range [][]string{
		{"action", "auth/refresh", "--path", "/yenile"},
		{"document", "humans", "--path", "/humans.txt"},
	} {
		code, out, stderr := add(t, dir, args...)
		if code != 0 {
			t.Fatalf("add %v = %d; %s", args, code, stderr)
		}
		if !strings.Contains(out, "register it in routes.go yourself") {
			t.Errorf("add %v did not say to register it by hand:\n%s", args, out)
		}
	}
	if after := readFile(t, dir, "routes.go"); after != before {
		t.Errorf("routes.go was edited:\n%s", after)
	}
	wantInOrder(t, readFile(t, dir, "actions/auth.go"), "func Logout()", "func Refresh()", `WithPath("tr", "/yenile")`)
	wantInOrder(t, readFile(t, dir, "actions/funcs/auth.go"), "\t\"context\"\n\t\"net/http\"\n", "func Logout()", "func Refresh()")
}

func TestAdd_TemplateFileUsesTheConfiguredRoot(t *testing.T) {
	dir := sdyProject(t)
	if code, _, stderr := add(t, dir, "page", "panel/help", "--file"); code != 0 {
		t.Fatalf("add = %d; %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "views", "pages", "panel", "help.tmpl")); err != nil {
		t.Errorf("the template is not under Template.Root with its Extension: %v", err)
	}
	if !strings.Contains(readFile(t, dir, "fragments/pages/panel/help.go"), `collage.NewFragment("help", "pages/panel/help.tmpl")`) {
		t.Error("the fragment does not name its template")
	}
}

// One line stays one line, and an empty Register call gets its first item.
func TestAdd_InsertsIntoListsOfEveryShape(t *testing.T) {
	for name, tt := range map[string]struct{ register, want string }{
		"one-line literal": {
			register: "\tfor _, p := range []*collage.Page{landingpages.Home()} {\n\t\t_ = app.RegisterPage(p)\n\t}\n\treturn nil",
			want:     "[]*collage.Page{landingpages.Home(), blogpages.Post()}",
		},
		"one-line call": {
			register: "\treturn app.Register(landingpages.Home())",
			want:     "app.Register(landingpages.Home(), blogpages.Post())",
		},
		"empty call": {
			register: "\treturn app.Register()",
			want:     "app.Register(blogpages.Post())",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{
				"go.mod": "module site\n",
				"routes.go": "package main\n\nimport (\n\tlandingpages \"site/pages/landing\"\n\n\t\"github.com/Elagoht/collage/pkg/collage\"\n)\n\nfunc register(app *collage.App) error {\n" +
					tt.register + "\n}\n",
			})
			if code, _, stderr := add(t, dir, "page", "blog/post"); code != 0 {
				t.Fatalf("add = %d; %s", code, stderr)
			}
			routes := readFile(t, dir, "routes.go")
			if !strings.Contains(routes, tt.want) {
				t.Errorf("routes.go lacks %s:\n%s", tt.want, routes)
			}
			wantInOrder(t, routes, `blogpages "site/pages/blog"`, `landingpages "site/pages/landing"`, `"github.com/Elagoht/collage/pkg/collage"`)
		})
	}
}

// A refusal writes nothing at all.
func TestAdd_RefusesBeforeWritingAnything(t *testing.T) {
	for name, tt := range map[string]struct {
		args  []string
		setup map[string]string
		want  string
	}{
		"a page name the project uses": {args: []string{"page", "account/login"}, want: `a page named "login" already exists, in pages/auth/login.go`},
		"an existing file":             {args: []string{"page", "panel/settings"}, setup: map[string]string{"pages/panel/settings.go": "package pages\n"}, want: "exists; collage add does not overwrite"},
		"a declared identifier":        {args: []string{"action", "auth/logout", "--name", "sign-out"}, want: "actions/auth.go already declares Logout"},
		"a name that is not one":       {args: []string{"page", "Blog/post"}, want: "is not a name"},
		"a path without a slash":       {args: []string{"page", "blog", "--path", "blog"}, want: "must start with /"},
		"a document in an area":        {args: []string{"document", "feeds/rss"}, want: "a document has no area"},
		"no kind":                      {args: []string{"widget", "x"}, want: "want a kind"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := sdyProject(t)
			writeFiles(t, dir, tt.setup)
			before := snapshot(t, dir)

			code, _, stderr := add(t, dir, tt.args...)
			if code == 0 {
				t.Fatal("add = 0, want a refusal")
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want %q", stderr, tt.want)
			}
			if after := snapshot(t, dir); after != before {
				t.Errorf("a refused add changed the project:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// snapshot returns every file under dir with its content, as one string.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		b.WriteString(path + "=" + string(content) + "\n")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestNames(t *testing.T) {
	for name, want := range map[string][2]string{
		"post":         {"Post", "post"},
		"story-detail": {"StoryDetail", "storyDetail"},
		"type":         {"Type", "type"},
		"a2-b3":        {"A2B3", "a2B3"},
	} {
		if got := [2]string{pascal(name), camel(name)}; got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

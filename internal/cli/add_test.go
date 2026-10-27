package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/plugin"
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
		{"fragment", "layouts/partials/siteFooter", "--file"},
		{"page", "blog/storyDetail"},
		{"page", "user-settings/profile"},
		{"fragment", "admin-tools/widget"},
		{"action", "blog/comment"},
		{"action", "blog/ping", "--path", "/api/ping"},
		{"document", "feed", "--path", "/feed.xml", "--type", "application/xml"},
		{"layout", "panel"},
		{"guard", "staff"},
		{"page", "staff/roster", "--guard", "staff", "--layout", "panel"},
		{"layout", "vault", "--guard", "staff"},
		{"page", "vaulted", "--layout", "vault"},
		{"page", "panelled", "--layout", "panel"},
		{"action", "staff/promote", "--page", "panelled"},
		{"middleware", "stamp"},
		{"middleware", "later"},
		{"command", "greet", "--name", "say-hi"},
		{"action", "staff/fire", "--path", "/api/fire", "--guard", "staff"},
	} {
		if code, _, stderr := add(t, target, args...); code != 0 {
			t.Fatalf("add %v = %d; %s", args, code, stderr)
		}
	}

	// The generated middleware passes requests on; make it mark them, so the
	// project's test can see it runs.
	writeFiles(t, target, map[string]string{"middleware/stamp.go": `package middleware

import "net/http"

func Stamp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Stamp", "1")
		next.ServeHTTP(w, r)
	})
}
`})

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
	// A fragment goes where its path says, not under pages/, in a package named
	// after its directory, with its name as it was written.
	wantInOrder(t, readFile(t, target, "fragments/blog/sidebar.go"), "package blog", "func Sidebar() *collage.Fragment")
	wantInOrder(t, readFile(t, target, "fragments/layouts/partials/siteFooter.go"),
		"package partials", "func SiteFooter() *collage.Fragment", `collage.NewFragment("siteFooter", "layouts/partials/siteFooter.html")`)
	if _, err := os.Stat(filepath.Join(target, "templates", "layouts", "partials", "siteFooter.html")); err != nil {
		t.Errorf("the fragment's template is not beside its path: %v", err)
	}
	// A dashed directory is only a path: its package and alias drop the dashes.
	wantInOrder(t, readFile(t, target, "fragments/admin-tools/widget.go"), "package admintools", "func Widget() *collage.Fragment")
	if !strings.Contains(routes, `usersettingspages "collageaddtest/pages/user-settings"`) || !strings.Contains(routes, "usersettingspages.Profile(),") {
		t.Errorf("the dashed area is not registered under a dashless alias:\n%s", routes)
	}
	wantInOrder(t, readFile(t, target, "pages/blog/storyDetail.go"), "func StoryDetail() *collage.Page", `collage.NewPage("storyDetail")`, `WithPath("en", "/blog/storyDetail")`)
	// A guard is its function alone; --guard puts it on a page's content or on
	// a layout with WithGuard, and wraps nothing in a layout of its own.
	wantInOrder(t, readFile(t, target, "guards/staff.go"), "package guards", "func Staff(", "Status: http.StatusForbidden")
	if _, err := os.Stat(filepath.Join(target, "fragments", "layouts", "staff.go")); !os.IsNotExist(err) {
		t.Errorf("add guard wrote a layout: %v", err)
	}
	wantInOrder(t, readFile(t, target, "pages/staff/roster.go"), "WithLayouts(layouts.Master(), layouts.Panel())")
	wantInOrder(t, readFile(t, target, "fragments/pages/staff/roster.go"), `"collageaddtest/guards"`, "func Roster() *collage.Fragment", "WithGuard(guards.Staff).", "WithData(rosterData())")
	wantInOrder(t, readFile(t, target, "fragments/layouts/vault.go"), `"collageaddtest/guards"`, "//collage:nested", "func Vault() *collage.Fragment", "WithGuard(guards.Staff).", `{{slot "content"}}`)
	wantInOrder(t, readFile(t, target, "pages/vaulted.go"), "WithLayouts(layouts.Master(), layouts.Vault())")
	wantInOrder(t, readFile(t, target, "fragments/layouts/panel.go"), "//collage:nested", "func Panel() *collage.Fragment", `{{slot "content"}}`)
	// --page attaches the action before the chain's Build; middleware keeps
	// the order it was added in; commands are registered beside it.
	wantInOrder(t, readFile(t, target, "pages/panelled.go"), `"collageaddtest/actions"`, "WithActionFor(actions.Promote()).", "Build()")
	wantInOrder(t, readFile(t, target, "actions/staff.go"), `"collageaddtest/guards"`, "func Fire() *collage.Action", "WithGuard(guards.Staff).", "WithHandler(funcs.Fire())")
	wantInOrder(t, routes, `"collageaddtest/commands"`, `"collageaddtest/middleware"`,
		"func register(app *collage.App) error {", "app.RegisterCommand(commands.Greet())",
		"app.Use(middleware.Stamp)", "app.Use(middleware.Later)", "app.Register(")

	writeFiles(t, target, map[string]string{"added_test.go": `package main

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

func TestAdded(t *testing.T) {
	c := client(t)
	for target, want := range map[string]string{"/blog/post": "<h1>Post</h1>", "/ar%C5%9Fiv": "<h1>Archive</h1>", "/about": "<h1>About</h1>", "/blog/storyDetail": "<h1>Story detail</h1>"} {
		if res := c.Get(target).WantStatus(http.StatusOK); !strings.Contains(res.Body, want) {
			t.Errorf("%s: %s", target, res.Body)
		}
	}
	if res := c.Get("/feed.xml").WantStatus(http.StatusOK); res.Header.Get("Content-Type") != "application/xml" {
		t.Errorf("feed type = %q", res.Header.Get("Content-Type"))
	}
	// The new guard refuses everyone until its check is written, on a page and
	// on an action of its own URL alike: with a valid forgery token the action
	// would answer 204, and the guard answers 403 instead.
	c.Get("/staff/roster").WantStatus(http.StatusForbidden)
	c.Get("/vaulted").WantStatus(http.StatusForbidden)
	fire := c.Request(http.MethodPost, "/api/fire", nil)
	fire.Header.Set("X-CSRF-Token", c.Get("/features").CSRFToken())
	c.Do(fire).WantStatus(http.StatusForbidden)
	// A nested layout renders inside the site's, and an action attached with
	// --page answers at its page.
	panelled := c.Get("/panelled").WantStatus(http.StatusOK)
	if !strings.Contains(panelled.Body, "<html") || !strings.Contains(panelled.Body, "<div class=\"panel\">") || !strings.Contains(panelled.Body, "<h1>Panelled</h1>") {
		t.Errorf("/panelled: %s", panelled.Body)
	}
	if panelled.Header.Get("X-Stamp") != "1" {
		t.Errorf("the middleware did not run: X-Stamp %q", panelled.Header.Get("X-Stamp"))
	}
	post := c.Request(http.MethodPost, "/panelled", nil)
	post.Header.Set("X-CSRF-Token", c.Get("/features").CSRFToken())
	c.Do(post).WantStatus(http.StatusOK)
	app, err := newApp(collage.Env{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())
	var names []string
	for _, cmd := range app.Commands() {
		names = append(names, cmd.Name)
	}
	if !slices.Contains(names, "say-hi") {
		t.Errorf("commands = %v, want say-hi", names)
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
		"fragments/layouts/master.go": `package layouts

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
			register: "\tfor _, p := range []*collage.Page{pages.Home()} {\n\t\t_ = app.RegisterPage(p)\n\t}\n\treturn nil",
			want:     "[]*collage.Page{pages.Home(), blogpages.Post()}",
		},
		"one-line call": {
			register: "\treturn app.Register(pages.Home())",
			want:     "app.Register(pages.Home(), blogpages.Post())",
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
				"routes.go": "package main\n\nimport (\n\t\"site/pages\"\n\n\t\"github.com/Elagoht/collage/pkg/collage\"\n)\n\nfunc register(app *collage.App) error {\n" +
					tt.register + "\n}\n",
			})
			if code, _, stderr := add(t, dir, "page", "blog/post"); code != 0 {
				t.Fatalf("add = %d; %s", code, stderr)
			}
			routes := readFile(t, dir, "routes.go")
			if !strings.Contains(routes, tt.want) {
				t.Errorf("routes.go lacks %s:\n%s", tt.want, routes)
			}
			wantInOrder(t, routes, `"site/pages"`, `blogpages "site/pages/blog"`, `"github.com/Elagoht/collage/pkg/collage"`)
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
		"a page name the project uses":   {args: []string{"page", "account/login"}, want: `a page named "login" already exists, in pages/auth/login.go`},
		"an existing file":               {args: []string{"page", "panel/settings"}, setup: map[string]string{"pages/panel/settings.go": "package pages\n"}, want: "exists; collage add does not overwrite"},
		"a declared identifier":          {args: []string{"action", "auth/logout", "--name", "sign-out"}, want: "actions/auth.go already declares Logout"},
		"a name that is not one":         {args: []string{"page", "blog/story-detail"}, want: `"story-detail" is not a Go name`},
		"a name that cannot export":      {args: []string{"fragment", "_hidden"}, want: "is not a Go name"},
		"a directory that is not one":    {args: []string{"fragment", "9x/widget"}, want: `"9x" is not a directory name`},
		"a path without a slash":         {args: []string{"page", "blog", "--path", "blog"}, want: "must start with /"},
		"a document in an area":          {args: []string{"document", "feeds/rss"}, want: "a document has no area"},
		"no kind":                        {args: []string{"widget", "x"}, want: "want a kind"},
		"a guard in an area":             {args: []string{"guard", "admin/staff"}, want: "a guard has no area"},
		"a guard name in use":            {args: []string{"guard", "staff"}, setup: map[string]string{"guards/staff.go": "package guards\n\nfunc Staff() {}\n"}, want: "guards/staff.go already declares Staff"},
		"--guard with no guard":          {args: []string{"page", "panel/users", "--guard", "staff"}, want: "guards/ declares no Staff; write it first with: collage add guard staff"},
		"--guard on a layout, no guard":  {args: []string{"layout", "vault", "--guard", "staff"}, want: "guards/ declares no Staff; write it first with: collage add guard staff"},
		"--guard on a fragment":          {args: []string{"fragment", "panel/widget", "--guard", "staff"}, want: "--guard guards a page, a layout or an action"},
		"--layout on an action":          {args: []string{"action", "auth/forget", "--layout", "staff"}, want: "--layout wraps a page"},
		"--guard that is not a name":     {args: []string{"page", "panel/users", "--guard", "st-aff"}, want: `--guard "st-aff" is not a Go name`},
		"a layout in an area":            {args: []string{"layout", "admin/panel"}, want: "a layout has no area; fragments/layouts/ is flat"},
		"a middleware in an area":        {args: []string{"middleware", "auth/load"}, want: "a middleware has no area"},
		"--layout with no layout":        {args: []string{"page", "panel/users", "--layout", "wide"}, want: "declares no Wide(); write it first with: collage add layout wide"},
		"--page with --path":             {args: []string{"action", "auth/forget", "--page", "auth/login", "--path", "/forget"}, want: "--page and --path"},
		"--page to no page":              {args: []string{"action", "auth/forget", "--page", "auth/nope"}, want: "pages/auth/nope.go does not exist"},
		"--page on a page":               {args: []string{"page", "panel/users", "--page", "auth/login"}, want: "--page attaches an action"},
		"--guard on an action, no guard": {args: []string{"action", "auth/forget", "--path", "/forget", "--guard", "staff"}, want: "guards/ declares no Staff; write it first with: collage add guard staff"},
		"a command --name with a space":  {args: []string{"command", "seed", "--name", "db seed"}, want: `--name "db seed" is not one shell word`},
		"a command --name like a flag":   {args: []string{"command", "seed", "--name", "-seed"}, want: `--name "-seed" is not one shell word`},
		"a command --name with a $":      {args: []string{"command", "seed", "--name", "se$ed"}, want: `--name "se$ed" is not one shell word`},
		"a command name in use":          {args: []string{"command", "seedAgain", "--name", "seed"}, setup: map[string]string{"commands/seed.go": "package commands\n\nimport \"github.com/Elagoht/collage/pkg/collage\"\n\nfunc Seed() collage.Command { return collage.Command{Name: \"seed\"} }\n"}, want: `a command named "seed" already exists, in commands/seed.go`},
	} {
		t.Run(name, func(t *testing.T) {
			dir := sdyProject(t)
			writeFiles(t, dir, tt.setup)
			before := treeSnapshot(t, dir)

			code, _, stderr := add(t, dir, tt.args...)
			if code == 0 {
				t.Fatal("add = 0, want a refusal")
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want %q", stderr, tt.want)
			}
			if after := treeSnapshot(t, dir); after != before {
				t.Errorf("a refused add changed the project:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// treeSnapshot returns every file under dir with its content, as one string.
func treeSnapshot(t *testing.T, dir string) string {
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

// A project whose site layout is Base, not Master, still wraps a new page in
// Base after a nested layout is added: it nests, and sorts first. A guard adds
// no layout at all.
func TestAdd_NestedLayoutsAreNeverTheSiteLayout(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":                    "module proj\n\ngo 1.26\n",
		"routes.go":                 "package main\n\nimport \"github.com/Elagoht/collage/pkg/collage\"\n\nfunc register(app *collage.App) error {\n\treturn app.Register()\n}\n",
		"fragments/layouts/base.go": "package layouts\n\nimport \"github.com/Elagoht/collage/pkg/collage\"\n\nfunc Base() *collage.Fragment { return nil }\n",
	})
	for _, args := range [][]string{{"guard", "admin"}, {"layout", "aside"}, {"page", "plain"}, {"page", "secret", "--guard", "admin"}} {
		if code, _, stderr := add(t, dir, args...); code != 0 {
			t.Fatalf("add %v = %d; %s", args, code, stderr)
		}
	}
	wantInOrder(t, readFile(t, dir, "pages/plain.go"), "WithLayouts(layouts.Base())")
	wantInOrder(t, readFile(t, dir, "pages/secret.go"), "WithLayouts(layouts.Base())")
	wantInOrder(t, readFile(t, dir, "fragments/pages/secret.go"), `"proj/guards"`, "WithGuard(guards.Admin).")
}

// Middleware and commands go into the register function whatever its shape —
// here a loop over a literal — after the calls already there; a register
// function that returns no error gets nothing, and the line to add instead.
func TestAdd_RegistersMiddlewareAndCommandsInAnyShape(t *testing.T) {
	dir := sdyProject(t)
	for _, args := range [][]string{{"middleware", "loadUser"}, {"middleware", "audit"}, {"command", "seed"}} {
		if code, _, stderr := add(t, dir, args...); code != 0 {
			t.Fatalf("add %v = %d; %s", args, code, stderr)
		}
	}
	wantInOrder(t, readFile(t, dir, "routes.go"),
		`"sdy/commands"`, `"sdy/middleware"`, "userService *users.UserService,\n) error {",
		"if err := app.RegisterCommand(commands.Seed()); err != nil {",
		"if err := app.Use(middleware.LoadUser); err != nil {", "if err := app.Use(middleware.Audit); err != nil {",
		"for _, page := range []*collage.Page{")

	noError := t.TempDir()
	writeFiles(t, noError, map[string]string{
		"go.mod":    "module ne\n\ngo 1.26\n",
		"routes.go": "package main\n\nimport \"github.com/Elagoht/collage/pkg/collage\"\n\nfunc register(app *collage.App) {\n\tapp.Register()\n}\n",
	})
	code, out, stderr := add(t, noError, "middleware", "audit")
	if code != 0 || !strings.Contains(out, "register it yourself, in the function that builds the app: app.Use(middleware.Audit)") {
		t.Errorf("add = %d; %s%s", code, out, stderr)
	}
	if strings.Contains(readFile(t, noError, "routes.go"), "middleware") {
		t.Error("routes.go was edited although its register returns no error")
	}
}

func TestNames(t *testing.T) {
	for name, want := range map[string][3]string{
		"post":        {"Post", "post", "Post"},
		"storyDetail": {"StoryDetail", "storyDetail", "Story detail"},
		"SiteFooter":  {"SiteFooter", "siteFooter", "Site footer"},
		"aboutAPI":    {"AboutAPI", "aboutAPI", "About API"},
		"type":        {"Type", "type", "Type"},
		"a2_b3":       {"A2_b3", "a2_b3", "A2 b3"},
		"çay":         {"Çay", "çay", "Çay"},
	} {
		g := generator{req: addRequest{name: name}}
		if got := [3]string{pascal(name), camel(name), g.title()}; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// A command's --name is checked by the rule RegisterCommand applies, so add
// writes no command the application refuses at start, and refuses none it
// accepts: a name in any script is one shell word.
func TestAdd_CommandNameIsRegisterCommandsRule(t *testing.T) {
	for name, ok := range map[string]bool{
		"çalıştır": true, "seed-db": true, "db:seed": true, "v2": true, "名前": true,
		"db seed": false, "-seed": false, "_seed": false, "se;ed": false, "se\u0000ed": false, "se\ted": false,
	} {
		req := addRequest{kind: "command", ident: name}
		err := req.parseTarget("seed")
		if ok != (err == nil) {
			t.Errorf("command --name %q: %v", name, err)
		}
		if ok != plugin.CommandWord(name) {
			t.Errorf("plugin.CommandWord(%q) = %v", name, !ok)
		}
	}
}

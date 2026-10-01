package collagetest_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// loginForm stands in for what a site's plugins put into a form: the forgery
// token, a hidden stamp of the kind a honeypot signs, and a trap field a bot
// would fill and a reader never sees.
const loginForm collage.InlineHTML = `<form action="{{pageURL "login"}}" method='POST'>
	{{csrfToken}}
	<input type=hidden name=stamp value="s&amp;1">
	<input type="text" name="trap" value="">
	<input name="email" value="">
	<button>Giriş yap</button>
</form>
<form action="/arama" method="get"><input type="hidden" name="kaynak" value="giriş"><input name="q"></form>`

// profileForm is a form sent as multipart/form-data, as one with a file is.
const profileForm collage.InlineHTML = `<form method="post" enctype="multipart/form-data">{{csrfToken}}<textarea name="bio"></textarea></form>`

// session is the cookie the login action sets and the panel reads.
const session = "oturum"

// errNoSession is the panel's refusal of a reader who has not signed in.
var errNoSession = errors.New("no session")

// panelView is what the panel greets the reader with.
type panelView struct {
	Name string
}

// newApp returns a site with a login page whose action checks what a reader's
// browser would have sent, a panel behind the cookie it sets, and a search the
// login page's second form submits to.
func newApp(t *testing.T) http.Handler {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/unused.html": {Data: []byte(``)}}, Root: "t"},
		Security: collage.SecurityConfig{CSRFKey: bytes.Repeat([]byte("k"), 32)},
		Locale:   collage.LocaleConfig{Default: "tr"},
	})
	if err != nil {
		t.Fatal(err)
	}

	login := collage.NewAction("login").
		WithMethods(http.MethodPost).
		WithHandler(func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			if err := rc.Request.ParseForm(); err != nil {
				return nil, err
			}
			form := rc.Request.PostForm
			if form.Get("stamp") != "s&1" || form.Get("trap") != "" || form.Get("email") == "" {
				return &collage.ActionResult{Status: http.StatusUnprocessableEntity, Body: []byte("refused: " + form.Encode())}, nil
			}
			result := collage.SeeOther("/panel")
			result.Header = http.Header{"Set-Cookie": {(&http.Cookie{Name: session, Value: url.QueryEscape(form.Get("email")), Path: "/"}).String()}}
			return result, nil
		}).
		Build()

	panel := collage.NewInlineFragment("panel", `<p>Hoş geldin, {{.Name}}</p>`).
		WithDataHandler(collage.Load(func(_ context.Context, rc *collage.RenderContext) (panelView, error) {
			cookie, err := rc.Request.Cookie(session)
			if err != nil {
				return panelView{}, errNoSession
			}
			name, err := url.QueryUnescape(cookie.Value)
			if err != nil {
				return panelView{}, err
			}
			return panelView{Name: name}, nil
		})).
		Required().
		Build()

	search := collage.NewInlineFragment("search", `<p>{{.}}</p>`).
		WithDataHandler(collage.Load(func(_ context.Context, rc *collage.RenderContext) (string, error) {
			return rc.Request.URL.Query().Get("kaynak") + ":" + rc.Request.URL.Query().Get("q"), nil
		})).
		Build()

	for _, page := range []*collage.Page{
		collage.NewPage("login").
			WithContent(collage.NewInlineFragment("login-form", loginForm).Build()).
			WithPath("tr", "/giriş").
			WithActionFor(login).
			Build(),
		collage.NewPage("panel").WithContent(panel).WithPath("tr", "/panel").Build(),
		collage.NewPage("search").WithContent(search).WithPath("tr", "/arama").Build(),
		collage.NewPage("profile").
			WithContent(collage.NewInlineFragment("profile-form", profileForm).Build()).
			WithPath("tr", "/profil").
			WithActionFor(collage.NewAction("profile").
				WithMethods(http.MethodPost).
				WithMaxBodyBytes(1 << 20).
				WithHandler(func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
					if err := rc.Request.ParseMultipartForm(1 << 20); err != nil {
						return nil, err
					}
					return collage.JSON(http.StatusOK, []byte(`"`+rc.Request.FormValue("bio")+`"`)), nil
				}).
				Build()).
			Build(),
	} {
		if err := app.RegisterPage(page); err != nil {
			t.Fatal(err)
		}
	}
	return app.Handler()
}

// The whole flow a reader goes through: the form's token and stamp travel with
// the submission, the trap stays empty, the cookie the action sets is sent to the
// page it redirects to.
func TestSubmitCarriesTheFormAndTheCookies(t *testing.T) {
	c := collagetest.New(t, newApp(t))

	page := c.Get("/giri%C5%9F").WantStatus(http.StatusOK)
	res := c.Submit(page, "/giriş", url.Values{"email": {"İlkşğ"}}).WantStatus(http.StatusSeeOther)

	if res.Location() != "/panel" {
		t.Fatalf("Location = %q, want /panel", res.Location())
	}
	panel := c.Follow(res).WantStatus(http.StatusOK)
	if !strings.Contains(panel.Body, "Hoş geldin, İlkşğ") {
		t.Errorf("the panel does not greet the submitted name:\n%s", panel.Body)
	}
}

// The forgery check is real: the same submission from a reader with no token is
// refused, which is what Submit saves a test from having to arrange.
func TestSubmitWithoutTheTokenIsRefused(t *testing.T) {
	c := collagetest.New(t, newApp(t))

	req := c.Request(http.MethodPost, "/giri%C5%9F", strings.NewReader(url.Values{"stamp": {"s&1"}, "email": {"a"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Do(req).WantStatus(http.StatusForbidden)
}

// The header path a script takes: the page's token, sent as X-CSRF-Token.
func TestCSRFTokenInAHeader(t *testing.T) {
	c := collagetest.New(t, newApp(t))
	page := c.Get("/giri%C5%9F")

	req := c.Request(http.MethodPost, "/giri%C5%9F", strings.NewReader(url.Values{"stamp": {"s&1"}, "email": {"a"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", page.CSRFToken())
	c.Do(req).WantStatus(http.StatusSeeOther)
}

// A GET form puts its hidden inputs and values in the query.
func TestSubmitAGetForm(t *testing.T) {
	c := collagetest.New(t, newApp(t))
	page := c.Get("/giri%C5%9F")

	res := c.Submit(page, "/arama", url.Values{"q": {"ağaç"}}).WantStatus(http.StatusOK)
	if !strings.Contains(res.Body, "giriş:ağaç") {
		t.Errorf("the search did not get the query:\n%s", res.Body)
	}
}

// Each Client is a reader of its own: the cookie one is given is not another's.
func TestClientsDoNotShareCookies(t *testing.T) {
	h := newApp(t)
	first := collagetest.New(t, h)
	res := first.Submit(first.Get("/giri%C5%9F"), "/giriş", url.Values{"email": {"a"}})
	first.Follow(res).WantStatus(http.StatusOK)

	second := collagetest.New(t, h)
	second.Get("/panel").WantStatus(http.StatusInternalServerError)
}

// A multipart form is sent as multipart; a form with no action submits to its
// page, and the page's only form needs no action named.
func TestSubmitAMultipartForm(t *testing.T) {
	c := collagetest.New(t, newApp(t))

	res := c.Submit(c.Get("/profil"), "", url.Values{"bio": {"Şiir yazarım"}}).WantStatus(http.StatusOK)
	if res.Body != `"Şiir yazarım"` {
		t.Errorf("body = %s, want the bio back", res.Body)
	}
}

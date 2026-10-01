package collage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

// checkedApp returns a started application whose templates link to what it
// registers, correctly in good.html and wrongly in bad.html and in an inline
// template: Turkish by default, English supported.
func checkedApp(t *testing.T, good, bad, inline string) *App {
	t.Helper()
	app, err := New(&Config{
		Server: ServerConfig{Host: "localhost", Port: 3000},
		Template: TemplateConfig{FS: fstest.MapFS{
			"t/good.html": {Data: []byte(good)},
			"t/bad.html":  {Data: []byte(bad)},
		}, Root: "t"},
		Locale: LocaleConfig{Default: "tr", Supported: []string{"tr", "en"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	clock := NewInlineFragment("clock", `<p>saat</p>`).Build()
	logout := NewAction("logout").WithPath("tr", "/çıkış").WithMethods(http.MethodPost).
		WithHandler(func(context.Context, *RenderContext) (*ActionResult, error) { return SeeOther("/"), nil }).Build()
	err = app.Register(
		NewPage("home").
			WithContent(NewFragment("good", "good.html").Build()).
			WithPath("tr", "/").
			WithFragmentPath("tr", "/parca/saat", clock).
			Build(),
		NewPage("post").WithContent(NewFragment("bad", "bad.html").Build()).WithPath("tr", "/yazi/{slug}").WithPath("en", "/post/{slug}").Build(),
		NewPage("about").WithContent(NewInlineFragment("about", inline).Build()).WithPath("en", "/about").Build(),
		NewDocument("feed", "application/xml").WithPath("tr", "/akis.xml").WithBody([]byte("<feed/>")).Build(),
		logout,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestCheckPassesWhatRenders(t *testing.T) {
	good := `{{pageURL "home"}} {{pageURL "post" "slug" .Slug}} {{pageURL "feed"}}
{{pageURL "about"}} {{pageURLIn "en" "about"}} {{pageURLIn "en" "post" "slug" "x"}}
{{actionURL "logout"}} {{fragmentURL "home" "clock"}} {{fragmentURLIn "tr" "home" "clock"}}
{{localeURL "en"}} {{pageURL .Name}} {{pageURL "post" .Key .Value}}
{{with pageURL "home"}}{{.}}{{end}} {{printf "%s" (pageURL "home")}}`
	app := checkedApp(t, good, `<p></p>`, `<p></p>`)

	if findings := app.Check(); len(findings) != 0 {
		t.Errorf("Check() = %v, want none: every link here renders", findings)
	}
}

func TestCheckFindsWhatFailsToRender(t *testing.T) {
	bad := `{{pageURL "hme"}}
{{pageURL "post"}}
{{pageURL "post" "slug"}}
{{pageURL "home" "page" "2"}}
{{actionURL "logut"}}
{{fragmentURL "home" "clok"}}
{{pageURLIn "de" "home"}}
{{pageURLIn "en" "home"}}
{{if true}}{{localeURL "fr"}}{{end}}`
	inline := `<a href="{{pageURL "abuot"}}">x</a>`
	app := checkedApp(t, `<p></p>`, bad, inline)

	findings := app.Check()
	want := []struct{ rule, contains string }{
		{RuleUnknownRoute, `bad.html:1:2: {{pageURL "hme"}}`},
		{RuleRouteParams, `bad.html:2:2: {{pageURL "post"}}`},
		{RuleRouteParams, `bad.html:3:2: {{pageURL "post" "slug"}}: its parameters are not name and value pairs`},
		{RuleRouteParams, `{{pageURL "home" "page" "2"}}`},
		{RuleUnknownRoute, `{{actionURL "logut"}}: collage: no page or document by that name: no action named "logut"; did you mean "logout"?`},
		{RuleUnknownRoute, `{{fragmentURL "home" "clok"}}`},
		{RuleUnreachableLocale, `{{pageURLIn "de" "home"}}`},
		{RuleNoPathInLocale, `{{pageURLIn "en" "home"}}`},
		{RuleUnreachableLocale, `bad.html:9:13: {{localeURL "fr"}}: no URL can carry the locale "fr"`},
		// Templates in name order: an inline template's name sorts after the files.
		{RuleUnknownRoute, `inline template of fragment "about":1:11: {{pageURL "abuot"}}: collage: no page or document by that name: "abuot"; did you mean "about"?`},
	}
	if len(findings) != len(want) {
		t.Fatalf("Check() found %d, want %d:\n%s", len(findings), len(want), describeFindings(findings))
	}
	for i, w := range want {
		f := findings[i]
		if f.Rule != w.rule || !strings.Contains(f.Message, w.contains) || f.Level != FindingError {
			t.Errorf("finding %d = [%s] %s\nwant [%s] containing %s", i, f.Rule, f.Message, w.rule, w.contains)
		}
	}
	if !strings.Contains(findings[5].Message, `did you mean "clock"?`) {
		t.Errorf("the fragment's finding suggests nothing: %s", findings[5].Message)
	}
}

// describeFindings lists findings one a line, for a failure message.
func describeFindings(findings []Finding) string {
	var b strings.Builder
	for _, f := range findings {
		b.WriteString("[" + f.Rule + "] " + f.Message + "\n")
	}
	return b.String()
}

func TestCheckCommand(t *testing.T) {
	app := checkedApp(t, `<p></p>`, `{{pageURL "hme"}}`, `<p></p>`)

	var out bytes.Buffer
	code, err := check(app, &out, []string{"-json"})
	if code != 1 || err == nil {
		t.Errorf("check = %d, %v; want 1 and an error for a finding", code, err)
	}
	var printed []checkedFinding
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil || len(printed) != 1 || printed[0].Rule != RuleUnknownRoute || printed[0].Level != "error" {
		t.Errorf("-json printed %s (%v)", out.String(), err)
	}

	clean := checkedApp(t, `<p></p>`, `<p></p>`, `<p></p>`)
	out.Reset()
	if code, err := check(clean, &out, nil); code != 0 || err != nil || !strings.Contains(out.String(), "nothing found") {
		t.Errorf("check on a clean app = %d, %v, %q", code, err, out.String())
	}
	if code, _ := check(clean, &out, []string{"-yaml"}); code != 2 {
		t.Errorf("check -yaml = %d, want 2", code)
	}
}

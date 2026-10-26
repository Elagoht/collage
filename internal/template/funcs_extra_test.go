package template

import (
	"html/template"
	"strings"
	"testing"
)

// render parses src with DefaultFuncs, as an engine does, and executes it with
// data, so each function is exercised through html/template's own argument
// handling and contextual escaping rather than called directly.
func render(t *testing.T, src string, data any) (string, error) { // any: html/template's own data type
	t.Helper()
	tmpl, err := template.New("t").Funcs(DefaultFuncs()).Parse(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	var b strings.Builder
	err = tmpl.Execute(&b, data)
	return b.String(), err
}

func mustRender(t *testing.T, src string, data any) string { // any: html/template's own data type
	t.Helper()
	got, err := render(t, src, data)
	if err != nil {
		t.Fatalf("execute %q: %v", src, err)
	}
	return got
}

type jsonPost struct {
	Title string `json:"title"`
	Tags  []string
}

func TestJSON(t *testing.T) {
	post := jsonPost{Title: "a<b", Tags: []string{"x"}}
	for _, tc := range []struct {
		name, src, want string
	}{
		{"pretty by default, in a script it is raw JSON", `<script type="application/json">{{json .}}</script>`,
			"<script type=\"application/json\">{\n  \"title\": \"a\\u003cb\",\n  \"Tags\": [\n    \"x\"\n  ]\n}</script>"},
		{"four spaces", `<script type="application/json">{{json .Tags 4}}</script>`,
			"<script type=\"application/json\">[\n    \"x\"\n]</script>"},
		{"compact", `<script type="application/json">{{json . 2 false}}</script>`,
			`<script type="application/json">{"title":"a\u003cb","Tags":["x"]}</script>`},
		{"escaped as text in HTML", `<p>{{json .Tags 2 false}}</p>`, `<p>[&#34;x&#34;]</p>`},
		{"escaped in an attribute", `<div data-x="{{json .Tags 2 false}}"></div>`, `<div data-x="[&#34;x&#34;]"></div>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustRender(t, tc.src, post); got != tc.want {
				t.Errorf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestJSON_BadArguments(t *testing.T) {
	for _, src := range []string{
		`{{json . "2"}}`,      // spaces not an integer
		`{{json . 2 "yes"}}`,  // pretty not a bool
		`{{json . 2 true 1}}`, // too many
		`{{json . -1}}`,       // negative indent
		`{{json .Ch}}`,        // not encodable
	} {
		t.Run(src, func(t *testing.T) {
			if _, err := render(t, src, struct{ Ch chan int }{make(chan int)}); err == nil {
				t.Errorf("%s rendered without an error", src)
			}
		})
	}
}

func TestPre(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		data any // any: html/template's own data type
		want string
	}{
		{"a struct as JSON", `{{pre .}}`, jsonPost{Title: "t"}, "<pre>{\n  &#34;title&#34;: &#34;t&#34;,\n  &#34;Tags&#34;: null\n}</pre>"},
		{"a string as it is, escaped", `{{pre .}}`, "<b>hi</b>", "<pre>&lt;b&gt;hi&lt;/b&gt;</pre>"},
		{"bytes as text", `{{pre .}}`, []byte("raw"), "<pre>raw</pre>"},
		{"compact", `{{pre . 2 false}}`, []int{1, 2}, "<pre>[1,2]</pre>"},
		{"nil", `{{pre .}}`, nil, "<pre>null</pre>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustRender(t, tc.src, tc.data); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMath(t *testing.T) {
	for src, want := range map[string]string{
		`{{add 2 3}}`:       "5",
		`{{sub 2 3}}`:       "-1",
		`{{mul 4 3}}`:       "12",
		`{{div 7 2}}`:       "3",
		`{{mod 7 2}}`:       "1",
		`{{min 7 2}}`:       "2",
		`{{max 7 2}}`:       "7",
		`{{addf 1.5 2.25}}`: "3.75",
		`{{subf 1.5 2}}`:    "-0.5",
		`{{mulf 1.5 2}}`:    "3",
		`{{divf 3 2}}`:      "1.5",
		`{{.N | add 1}}`:    "42",
		`{{add .N .N}}`:     "82",
	} {
		if got := mustRender(t, src, struct{ N int }{41}); got != want {
			t.Errorf("%s = %q, want %q", src, got, want)
		}
	}
	for _, src := range []string{`{{div 1 0}}`, `{{mod 1 0}}`, `{{divf 1 0}}`} {
		if _, err := render(t, src, nil); err == nil {
			t.Errorf("%s rendered without an error", src)
		}
	}
}

func TestText(t *testing.T) {
	for src, want := range map[string]string{
		`{{trim "  a b  "}}`:                  "a b",
		`{{replace "a" "o" "banana"}}`:        "bonono",
		`{{contains "nan" "banana"}}`:         "true",
		`{{hasPrefix "ba" "banana"}}`:         "true",
		`{{hasSuffix "ba" "banana"}}`:         "false",
		`{{join "|" (split "," "a,b,c")}}`:    "a|b|c",
		`{{truncate 5 "Çekirdek"}}`:           "Çekir…",
		`{{truncate 8 "Çekirdek"}}`:           "Çekirdek",
		`{{truncate 0 "abc"}}`:                "…",
		`{{"a b" | pathEscape}}`:              "a%20b",
		`{{"  x " | trim | replace "x" "y"}}`: "y",
	} {
		if got := mustRender(t, src, nil); got != want {
			t.Errorf("%s = %q, want %q", src, got, want)
		}
	}
	if _, err := render(t, `{{truncate -1 "abc"}}`, nil); err == nil {
		t.Errorf("truncate -1 rendered without an error")
	}
}

type digUser struct {
	Name    string
	Profile *digProfile
	secret  string
}

type digProfile struct {
	City string
}

func TestCollections(t *testing.T) {
	data := struct {
		List  []string
		Empty []string
		Map   map[string]int
		Deep  map[string]map[string]string
		User  digUser
	}{
		List: []string{"a", "b", "c"},
		Map:  map[string]int{"b": 2, "a": 1},
		Deep: map[string]map[string]string{"x": {"y": "z"}},
		User: digUser{Name: "Ada", Profile: &digProfile{City: "İzmir"}, secret: "s"},
	}
	for src, want := range map[string]string{
		`{{first .List}}`:                         "a",
		`{{last .List}}`:                          "c",
		`{{first .Empty}}`:                        "",
		`{{if first .Empty}}x{{else}}none{{end}}`: "none",
		`{{range seq 3}}{{.}}{{end}}`:             "123",
		`{{range seq 0}}{{.}}{{end}}`:             "",
		`{{join "," (keys .Map)}}`:                "a,b",
		`{{hasKey .Map "a"}}`:                     "true",
		`{{hasKey .Map "z"}}`:                     "false",
		`{{dig .Deep "x" "y"}}`:                   "z",
		`{{dig .Deep "x" "nope"}}`:                "",
		`{{dig .Deep "nope" "y"}}`:                "",
		`{{dig .User "Profile" "City"}}`:          "İzmir",
		`{{dig .User "Name"}}`:                    "Ada",
		`{{dig .User "secret"}}`:                  "",
		`{{dig .User "Name" "deeper"}}`:           "",
	} {
		if got := mustRender(t, src, data); got != want {
			t.Errorf("%s = %q, want %q", src, got, want)
		}
	}
	for _, src := range []string{`{{first .Map}}`, `{{keys .List}}`, `{{hasKey .List "a"}}`, `{{seq 100001}}`} {
		if _, err := render(t, src, data); err == nil {
			t.Errorf("%s rendered without an error", src)
		}
	}
}

func TestWithQueryPlaceholder(t *testing.T) {
	if _, err := render(t, `{{withQuery "page" 2}}`, nil); err == nil {
		t.Errorf("withQuery outside a render did not fail")
	}
}

type digBase struct{ ID string }

type digWrapper struct{ *digBase }

// A promoted field through a nil embedded pointer is a path that leads
// nowhere, not a failed render.
func TestDig_NilEmbeddedPointer(t *testing.T) {
	if got := mustRender(t, `{{dig . "ID"}}`, digWrapper{}); got != "" {
		t.Errorf("dig through a nil embedded pointer = %q, want nothing", got)
	}
	if got := mustRender(t, `{{dig . "ID"}}`, digWrapper{&digBase{ID: "7"}}); got != "7" {
		t.Errorf("dig through an embedded pointer = %q, want 7", got)
	}
}

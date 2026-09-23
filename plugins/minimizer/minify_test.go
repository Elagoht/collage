package minimizer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMinifyJSON_IsExact(t *testing.T) {
	src := []byte(`{
		"name": "a  b",
		"list": [1, 2, 3],
		"nested": {"deep": true}
	}`)

	out := minifyJSON(src)
	if strings.Contains(string(out), "\n") || strings.Contains(string(out), "\t") {
		t.Errorf("output still has formatting: %s", out)
	}
	// Not "no double spaces": the value "a  b" contains two, and they are content.

	// The only real assertion: the value is unchanged.
	var before, after map[string]any // any: what encoding/json decodes into
	if err := json.Unmarshal(src, &before); err != nil {
		t.Fatalf("source is not valid JSON: %v", err)
	}
	if err := json.Unmarshal(out, &after); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(before) != len(after) || before["name"] != after["name"] {
		t.Errorf("value changed: %v -> %v", before, after)
	}
}

func TestMinifyJSON_LeavesInvalidInputAlone(t *testing.T) {
	src := []byte(`{"broken": `)
	if got := string(minifyJSON(src)); got != string(src) {
		t.Errorf("got %q, want the input returned untouched", got)
	}
}

func TestMinifyHTML_KeepsProtectedElementsVerbatim(t *testing.T) {
	// <pre> renders its whitespace and <script> holds another language. Collapsing
	// either is how a minifier silently breaks a page.
	src := []byte("<div>   <p>a</p>   </div>\n<pre>  keep\n   me  </pre>\n<script>\n  var a = 1;\n</script>")

	out := string(minifyHTML(src))

	if !strings.Contains(out, "<pre>  keep\n   me  </pre>") {
		t.Errorf("<pre> content was altered:\n%s", out)
	}
	if !strings.Contains(out, "var a = 1;") {
		t.Errorf("<script> content was altered:\n%s", out)
	}
	if strings.Contains(out, "<div>   <p>") {
		t.Errorf("whitespace between tags was not collapsed:\n%s", out)
	}
}

func TestMinifyHTML_DoesNotJoinWords(t *testing.T) {
	// The run between </b> and "world" renders as a space. Deleting it produces
	// "Helloworld", which is the most common way a naive minifier is noticed.
	src := []byte("<p><b>Hello</b>   world</p>")

	if got := string(minifyHTML(src)); !strings.Contains(got, "</b> world") {
		t.Errorf("got %q, want the separating space kept as one space", got)
	}
}

func TestMinifyHTML_KeepsConditionalComments(t *testing.T) {
	src := []byte("<!-- ordinary --><!--[if IE]><p>ie</p><![endif]-->")

	out := string(minifyHTML(src))
	if strings.Contains(out, "ordinary") {
		t.Error("an ordinary comment survived")
	}
	if !strings.Contains(out, "<![endif]") {
		t.Errorf("a conditional comment was removed: %q", out)
	}
}

func TestMinifyCSS_LeavesStringsAlone(t *testing.T) {
	// The two spaces in the content string are content.
	src := []byte(`.a { content: "x  y"; /* note */ color : red ; }`)

	// Spelled out rather than probed: the two spaces inside the content string are
	// content and must survive, the comment must not, and the spaces around ":"
	// must stay — "a :hover" and "a:hover" are a descendant selector and a
	// pseudo-class, and telling one colon from the other requires parsing.
	got := string(minifyCSS(src))
	want := `.a { content: "x  y"; color : red ; }`
	if got != want {
		t.Errorf("minifyCSS()\n got  %q\n want %q", got, want)
	}
}

func TestMinifyJS_KeepsEveryNewline(t *testing.T) {
	// Automatic semicolon insertion: "return\n  x" is "return; x". Joining those
	// lines changes what the program does, so no newline may be removed.
	src := []byte("function f() {\n    return\n    1\n}")

	out := string(minifyJS(src))
	if got, want := strings.Count(out, "\n"), strings.Count(string(src), "\n"); got != want {
		t.Errorf("newlines = %d, want %d — removing one can change what the program means:\n%q", got, want, out)
	}
	if strings.Contains(out, "    return") {
		t.Errorf("indentation was not removed: %q", out)
	}
}

func TestMinifyJS_DoesNotTreatAURLInAStringAsAComment(t *testing.T) {
	// The classic corruption: "//" inside a string starts no comment, and treating
	// it as one deletes the rest of the line.
	src := []byte(`var url = "https://example.com/x"; var after = 1;`)

	out := string(minifyJS(src))
	if !strings.Contains(out, `"https://example.com/x"`) {
		t.Errorf("the URL was mangled: %q", out)
	}
	if !strings.Contains(out, "after") {
		t.Errorf("code after the string was deleted: %q", out)
	}
}

func TestMinifyJS_HandlesRegexLiteralsContainingSlashes(t *testing.T) {
	src := []byte("var re = /https:\\/\\/[a-z/]+/g; var after = 2;")

	out := string(minifyJS(src))
	if !strings.Contains(out, "after") {
		t.Errorf("code after a regex literal was deleted: %q", out)
	}
}

func TestMinifyJS_HandlesTemplateLiterals(t *testing.T) {
	src := []byte("const s = `a  ${ b }  c`;\nconst after = 3;")

	out := string(minifyJS(src))
	if !strings.Contains(out, "a  ${") {
		t.Errorf("a template literal's whitespace was collapsed: %q", out)
	}
	if !strings.Contains(out, "after") {
		t.Errorf("code after a template literal was lost: %q", out)
	}
}

func TestMinifyJS_KeepsALineForAMultilineBlockComment(t *testing.T) {
	// The comment stood where a newline stood, and that newline may have been
	// terminating a statement.
	src := []byte("var a = 1\n/* a\n   comment */\nvar b = 2")

	out := string(minifyJS(src))
	if strings.Contains(out, "comment") {
		t.Errorf("the comment survived: %q", out)
	}
	if !strings.Contains(out, "var a = 1\n") || !strings.Contains(out, "\nvar b = 2") {
		t.Errorf("a statement boundary was lost: %q", out)
	}
}

func TestMinify_NeverGrowsInput(t *testing.T) {
	for name, fn := range map[string]func([]byte) []byte{
		"html": minifyHTML,
		"css":  minifyCSS,
		"js":   minifyJS,
		"json": minifyJSON,
	} {
		for _, src := range []string{"", " ", "a", "{}", "<p>x</p>", "/*", "//", `"`, "`"} {
			if got := fn([]byte(src)); len(got) > len(src) {
				t.Errorf("%s(%q) grew from %d to %d bytes", name, src, len(src), len(got))
			}
		}
	}
}

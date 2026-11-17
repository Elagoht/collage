package cli

import (
	"strings"
	"testing"
)

// The help texts say what the commands do; these are the sentences that were
// wrong, so they stay right.
func TestHelpText_SaysWhatTheCommandsDo(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		want, not  []string
	}{
		{"serve", serveUsage,
			[]string{"Cache-Control: no-store"}, []string{"no caching headers"}},
		{"export", exportUsage,
			[]string{"report when it is done", ".collage-export"}, []string{"as it writes it"}},
		{"new", newUsage,
			[]string{"Both templates write a .env.example, a .gitignore and a README"}, []string{"a .env.example for \"collage dev\", a\n            .gitignore"}},
	} {
		for _, want := range tt.want {
			if !strings.Contains(tt.text, want) {
				t.Errorf("%s help does not say %q:\n%s", tt.name, want, tt.text)
			}
		}
		for _, not := range tt.not {
			if strings.Contains(tt.text, not) {
				t.Errorf("%s help still says %q", tt.name, not)
			}
		}
	}
}

// The scaffold names one env file in every place it tells you to make one:
// ".env", which every way of running the project reads.
func TestScaffold_OneEnvFileInstruction(t *testing.T) {
	for _, tt := range []struct{ name, text string }{{"new usage", newUsage}} {
		if strings.Contains(tt.text, "cp .env.example .env.development") {
			t.Errorf("%s still copies .env.example to .env.development", tt.name)
		}
	}
	for _, path := range []string{"scaffold/demo/README.md.tmpl", "scaffold/minimal/README.md.tmpl"} {
		body, err := scaffoldFS.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "cp .env.example .env.development") {
			t.Errorf("%s copies .env.example to .env.development, while .env.example itself says .env", path)
		}
	}
}

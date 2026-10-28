package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestManagedSection_Version(t *testing.T) {
	got := string(managedSection("1.2.3"))
	if !strings.HasPrefix(got, "<!-- collage:begin v1.2.3 —") {
		t.Errorf("section starts %q, want the begin marker with v1.2.3", firstLine(got))
	}
	if !strings.HasSuffix(got, "<!-- collage:end -->\n") {
		t.Errorf("section does not end with the end marker and a newline")
	}
	if strings.Contains(got, "@@") {
		t.Errorf("section still holds a placeholder")
	}
}

// Small models skim: a rule past the first screens is a rule not read.
func TestManagedSection_LineBudget(t *testing.T) {
	if n := strings.Count(string(managedSection("1.0.0")), "\n"); n > 200 {
		t.Errorf("section is %d lines, want at most 200", n)
	}
}

// The rules name commands, kinds and bundles; a rename that leaves them behind
// would teach agents something that does not exist.
func TestManagedSection_NamesRealThings(t *testing.T) {
	got := string(managedSection("1.0.0"))
	for _, kind := range addKinds {
		if !strings.Contains(got, "`"+kind+"`") {
			t.Errorf("section does not name the add kind %q", kind)
		}
	}
	for _, b := range bundles {
		if !strings.Contains(got, "`@"+b.Name+"`") {
			t.Errorf("section does not name the bundle @%s", b.Name)
		}
	}
	for _, s := range []string{"collage plugin add", "collage check", "collage add layout", "validations/", "utils/", "Handler() collage.ActionHandlerFunc", "routes.go",
		"fragments/<area>/<name>.go", "WithDependencyTags", "session.RequireUser", "--page", "if err := rc.HoistStylesheet", "layout needs `--file`"} {
		if !strings.Contains(got, s) {
			t.Errorf("section does not mention %q", s)
		}
	}
}

func TestMergeAgents(t *testing.T) {
	section := managedSection("2.0.0")
	old := "<!-- collage:begin v1.0.0 — old -->\nold rules\n<!-- collage:end -->\n"
	tail := "\n## Project rules\nmine\n"

	for _, tc := range []struct {
		name     string
		existing []byte
		want     string
	}{
		{"no file", nil, string(section) + projectRulesStub},
		{"markers", []byte(old + tail), string(section) + tail},
		{"text before the markers", []byte("# Top\n\n" + old + tail), "# Top\n\n" + string(section) + tail},
		{"no markers", []byte("# Mine\nrule\n"), string(section) + "\n# Mine\nrule\n"},
		{"crlf markers", []byte(strings.ReplaceAll(old, "\n", "\r\n") + "mine\r\n"), string(section) + "mine\r\n"},
		{"marker text mid-line", []byte("see <!-- collage:begin here\n"), string(section) + "\nsee <!-- collage:begin here\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mergeAgents(tc.existing, section)
			if err != nil {
				t.Fatalf("mergeAgents() error = %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("mergeAgents() =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestMergeAgents_BadMarkers(t *testing.T) {
	begin, end := "<!-- collage:begin v1 -->\n", "<!-- collage:end -->\n"
	for name, existing := range map[string]string{
		"begin only":       begin + "x\n",
		"end only":         "x\n" + end,
		"end before begin": end + begin,
		"two begins":       begin + begin + end,
		"two sections":     begin + end + begin + end,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := mergeAgents([]byte(existing), managedSection("1")); !errors.Is(err, ErrAgentsMarkers) {
				t.Errorf("mergeAgents() error = %v, want ErrAgentsMarkers", err)
			}
		})
	}
}

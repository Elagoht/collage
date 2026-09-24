package collage

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func printed(t *testing.T, report *BuildReport, err error) string {
	t.Helper()
	var buf bytes.Buffer
	PrintBuildReport(&buf, report, err)
	return buf.String()
}

// A bytes.Buffer is not a terminal, so nothing escapes into it. Escape codes in a
// log file outlive the session that produced them and are noise in every reader
// that opens it afterwards.
func TestPrintBuildReport_NoColourWhenNotATerminal(t *testing.T) {
	out := printed(t, &BuildReport{Written: []string{"/tmp/x/index.html"}}, nil)

	if strings.Contains(out, "\x1b[") {
		t.Errorf("output carries escape codes:\n%q", out)
	}
	if strings.ContainsAny(out, "✓▲✗") {
		t.Errorf("output carries terminal symbols:\n%q", out)
	}
}

// A skip is the build saying a page is not in its output. It is the thing most
// worth noticing, so it is never truncated and never buried.
func TestPrintBuildReport_EverySkipIsNamed(t *testing.T) {
	report := &BuildReport{
		Written: []string{"/tmp/x/index.html"},
		Skipped: []SkipRecord{
			{Page: "signup", Reason: "page uses the dynamic render strategy"},
			{Page: "admin", Locale: "tr", Reason: "no path for this locale"},
		},
		Duration: 2 * time.Millisecond,
	}

	out := printed(t, report, nil)

	for _, want := range []string{"signup", "dynamic render strategy", "admin (tr)", "no path for this locale"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "2 pages skipped") {
		t.Errorf("output does not count the skips:\n%s", out)
	}
}

// A long list of written files is summarised, because a build that wrote three
// hundred of them must not bury the one it skipped.
func TestPrintBuildReport_LongFileListsAreTruncated(t *testing.T) {
	written := make([]string, 0, 40)
	for i := range 40 {
		written = append(written, "/tmp/x/page"+string(rune('a'+i%26))+string(rune('0'+i/26))+".html")
	}
	report := &BuildReport{
		Written: written,
		Skipped: []SkipRecord{{Page: "signup", Reason: "dynamic"}},
	}

	out := printed(t, report, nil)

	if !strings.Contains(out, "and 30 more") {
		t.Errorf("a 40-file list was not truncated:\n%s", out)
	}
	if !strings.Contains(out, "signup") {
		t.Errorf("the skip was lost among the files:\n%s", out)
	}
	if lines := strings.Count(out, "\n"); lines > 20 {
		t.Errorf("output is %d lines for a 40-file build, which is a wall", lines)
	}
}

// The last line says how it went, so it can be read without reading the rest.
func TestPrintBuildReport_SummaryCountsEverything(t *testing.T) {
	report := &BuildReport{
		Written:  []string{"/tmp/x/a.html", "/tmp/x/b.html"},
		Skipped:  []SkipRecord{{Page: "signup", Reason: "dynamic"}},
		Errors:   []error{errors.New("render failed")},
		Duration: 1500 * time.Millisecond,
	}

	out := printed(t, report, errors.New("render failed"))
	last := ""
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		last = line
	}

	for _, want := range []string{"2 written", "1 skipped", "1 failed", "1.5s"} {
		if !strings.Contains(last, want) {
			t.Errorf("summary = %q, want it to contain %q", last, want)
		}
	}
}

// A build that failed before recording anything still says so: the joined error is
// all there is, and printing nothing would report a failure as a quiet success.
func TestPrintBuildReport_FailureWithNoRecordedErrors(t *testing.T) {
	out := printed(t, &BuildReport{}, errors.New("output directory is a file"))

	if !strings.Contains(out, "build failed") {
		t.Errorf("a failed build printed no failure:\n%s", out)
	}
	if !strings.Contains(out, "output directory is a file") {
		t.Errorf("the reason is missing:\n%s", out)
	}
}

func TestPrintBuildReport_NilReportIsNotACrash(t *testing.T) {
	var buf bytes.Buffer
	PrintBuildReport(&buf, nil, nil)
	if buf.Len() != 0 {
		t.Errorf("output = %q, want nothing", buf.String())
	}
}

func TestPrintBuildReport_Warnings(t *testing.T) {
	var out strings.Builder
	PrintBuildReport(&out, &BuildReport{
		Written:  []string{"/tmp/dist/blogs/index.html"},
		Warnings: []WarningRecord{{Page: "blogs", Reason: "reads the query parameters page"}},
	}, nil)
	got := out.String()
	if !strings.Contains(got, "1 warning") || !strings.Contains(got, "blogs  reads the query parameters page") {
		t.Errorf("output = %q, want the warning named and counted", got)
	}
	if !strings.Contains(got, "1 written · 0 skipped · 0 failed · 1 warning") {
		t.Errorf("summary = %q, want the warning in the summary line", got)
	}
}

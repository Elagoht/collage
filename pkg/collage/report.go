package collage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Elagoht/collage/internal/term"
)

// maxListedFiles is how many written paths are named before the rest are counted.
//
// A build that writes three files should show them; one that writes three hundred
// should not bury what went wrong under them. Ten is enough to recognise a small
// site and few enough that a skip below stays on the screen.
const maxListedFiles = 10

// PrintBuildReport writes a static build's outcome to w.
//
// It exists in the framework rather than in each project because the thing that
// matters here is easy to get wrong and easy to miss: a skipped page and a failed
// one are what a build is telling you about, and a flat list of every file written
// is what hides them. So written files are summarised, while skips and failures are
// named, in that order, with the summary last — the line people actually read.
//
// Colour and the marker characters are used only when w is a terminal, and never
// when NO_COLOR is set or TERM says the terminal is dumb. Output redirected to a
// file or a CI log is plain ASCII, because escape codes in a log are noise that
// outlives the session that produced them.
func PrintBuildReport(w io.Writer, report *BuildReport, buildErr error) {
	if report == nil {
		return
	}
	s := term.NewStyle(w)

	printWritten(w, s, report.Written)
	printSkipped(w, s, report.Skipped)
	printWarnings(w, s, report.Warnings)
	printFindings(w, s, report.Findings)
	printErrors(w, s, report.Errors, buildErr)
	printSummary(w, s, report)
}

func printWritten(w io.Writer, s term.Style, written []string) {
	if len(written) == 0 {
		return
	}

	fmt.Fprintf(w, "\n%s %s\n", s.OK(s.Mark("✓", "+")), s.Bold(plural(len(written), "file", "files")+" written"))

	paths := relativise(written)
	sort.Strings(paths)
	shown := paths
	if len(shown) > maxListedFiles {
		shown = shown[:maxListedFiles]
	}
	for _, path := range shown {
		fmt.Fprintf(w, "    %s\n", s.Dim(path))
	}
	if rest := len(paths) - len(shown); rest > 0 {
		fmt.Fprintf(w, "    %s\n", s.Dim(fmt.Sprintf("… and %d more", rest)))
	}
}

func printSkipped(w io.Writer, s term.Style, skipped []SkipRecord) {
	if len(skipped) == 0 {
		return
	}

	// Never truncated. A skip is the build telling you a page is not in its
	// output, which is the thing most worth noticing and the easiest to miss.
	// Counted as routes, not pages: documents are skipped for the same reasons.
	fmt.Fprintf(w, "\n%s %s\n", s.Warn(s.Mark("▲", "!")), s.Bold(fmt.Sprintf("%d skipped", len(skipped))))
	for _, skip := range skipped {
		name := skip.Page
		if skip.Locale != "" {
			name += " (" + skip.Locale + ")"
		}
		fmt.Fprintf(w, "    %s  %s\n", s.Warn(name), s.Dim(skip.Reason))
	}
}

func printWarnings(w io.Writer, s term.Style, warnings []WarningRecord) {
	if len(warnings) == 0 {
		return
	}

	// Never truncated, for the same reason as a skip: each one is a page whose
	// file is not the whole of what the server answers.
	fmt.Fprintf(w, "\n%s %s\n", s.Warn(s.Mark("▲", "!")), s.Bold(plural(len(warnings), "warning", "warnings")))
	for _, warning := range warnings {
		fmt.Fprintf(w, "    %s  %s\n", s.Warn(warning.Page), s.Dim(warning.Reason))
	}
}

// printFindings lists what the checks found, grouped by page, errors first within
// each. Never truncated: a finding is only worth reporting if it is read.
func printFindings(w io.Writer, s term.Style, findings []Finding) {
	if len(findings) == 0 {
		return
	}
	sorted := append([]Finding(nil), findings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Path != sorted[j].Path {
			return sorted[i].Path < sorted[j].Path
		}
		return sorted[i].Level > sorted[j].Level
	})
	fmt.Fprintf(w, "\n%s %s\n", s.Warn(s.Mark("◆", "*")), s.Bold(plural(len(findings), "finding", "findings")))
	path := "\x00"
	for _, f := range sorted {
		if f.Path != path {
			path = f.Path
			label := path
			if label == "" {
				label = "(the build)"
			}
			fmt.Fprintf(w, "  %s\n", s.Bold(label))
		}
		level := s.Warn(f.Level.String())
		if f.Level == FindingError {
			level = s.Fail(f.Level.String())
		}
		fmt.Fprintf(w, "    %s %s  %s %s\n", level, f.Rule, f.Message, s.Dim(f.Plugin))
	}
}

func printErrors(w io.Writer, s term.Style, errs []error, buildErr error) {
	// buildErr is errors.Join of exactly report.Errors, so the list is what to
	// print — unless a build failed before it could record any, which is the one
	// case where the joined error is all there is.
	if len(errs) == 0 {
		if buildErr != nil {
			fmt.Fprintf(w, "\n%s %s\n    %s\n",
				s.Fail(s.Mark("✗", "x")), s.Bold("build failed"), s.Dim(buildErr.Error()))
		}
		return
	}

	fmt.Fprintf(w, "\n%s %s\n", s.Fail(s.Mark("✗", "x")), s.Bold(plural(len(errs), "failure", "failures")))
	for _, err := range errs {
		fmt.Fprintf(w, "    %s\n", s.Fail(err.Error()))
	}
}

func printSummary(w io.Writer, s term.Style, report *BuildReport) {
	parts := []string{
		fmt.Sprintf("%d written", len(report.Written)),
		fmt.Sprintf("%d skipped", len(report.Skipped)),
		fmt.Sprintf("%d failed", len(report.Errors)),
	}
	if len(report.Warnings) > 0 {
		parts = append(parts, plural(len(report.Warnings), "warning", "warnings"))
	}
	if len(report.Findings) > 0 {
		parts = append(parts, plural(len(report.Findings), "finding", "findings"))
	}
	parts = append(parts, round(report.Duration))
	line := strings.Join(parts, " · ")

	// Coloured by the worst thing in it, so the last line of the output says how
	// the build went without being read.
	switch {
	case len(report.Errors) > 0:
		line = s.Fail(line)
	case len(report.Skipped) > 0 || len(report.Warnings) > 0 || len(report.Findings) > 0:
		line = s.Warn(line)
	default:
		line = s.OK(line)
	}
	fmt.Fprintf(w, "\n%s\n", line)
}

// relativise shortens absolute paths against the working directory, which is where
// the reader is standing. A path that does not live under it is left alone.
func relativise(paths []string) []string {
	cwd, err := os.Getwd()
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if err == nil {
			if rel, relErr := filepath.Rel(cwd, path); relErr == nil && !strings.HasPrefix(rel, "..") {
				out = append(out, rel)
				continue
			}
		}
		out = append(out, path)
	}
	return out
}

// round trims a duration to something a person reads rather than parses.
func round(d time.Duration) string {
	switch {
	case d >= time.Second:
		return d.Round(10 * time.Millisecond).String()
	case d >= time.Millisecond:
		return d.Round(100 * time.Microsecond).String()
	default:
		return d.Round(time.Microsecond).String()
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

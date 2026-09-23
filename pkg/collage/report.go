package collage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	s := newStyle(w)

	printWritten(w, s, report.Written)
	printSkipped(w, s, report.Skipped)
	printErrors(w, s, report.Errors, buildErr)
	printSummary(w, s, report)
}

func printWritten(w io.Writer, s style, written []string) {
	if len(written) == 0 {
		return
	}

	fmt.Fprintf(w, "\n%s %s\n", s.ok(s.mark("✓", "+")), s.bold(plural(len(written), "file", "files")+" written"))

	paths := relativise(written)
	sort.Strings(paths)
	shown := paths
	if len(shown) > maxListedFiles {
		shown = shown[:maxListedFiles]
	}
	for _, path := range shown {
		fmt.Fprintf(w, "    %s\n", s.dim(path))
	}
	if rest := len(paths) - len(shown); rest > 0 {
		fmt.Fprintf(w, "    %s\n", s.dim(fmt.Sprintf("… and %d more", rest)))
	}
}

func printSkipped(w io.Writer, s style, skipped []SkipRecord) {
	if len(skipped) == 0 {
		return
	}

	// Never truncated. A skip is the build telling you a page is not in its
	// output, which is the thing most worth noticing and the easiest to miss.
	fmt.Fprintf(w, "\n%s %s\n", s.warn(s.mark("▲", "!")), s.bold(plural(len(skipped), "page", "pages")+" skipped"))
	for _, skip := range skipped {
		name := skip.Page
		if skip.Locale != "" {
			name += " (" + skip.Locale + ")"
		}
		fmt.Fprintf(w, "    %s  %s\n", s.warn(name), s.dim(skip.Reason))
	}
}

func printErrors(w io.Writer, s style, errs []error, buildErr error) {
	// buildErr is errors.Join of exactly report.Errors, so the list is what to
	// print — unless a build failed before it could record any, which is the one
	// case where the joined error is all there is.
	if len(errs) == 0 {
		if buildErr != nil {
			fmt.Fprintf(w, "\n%s %s\n    %s\n",
				s.fail(s.mark("✗", "x")), s.bold("build failed"), s.dim(buildErr.Error()))
		}
		return
	}

	fmt.Fprintf(w, "\n%s %s\n", s.fail(s.mark("✗", "x")), s.bold(plural(len(errs), "failure", "failures")))
	for _, err := range errs {
		fmt.Fprintf(w, "    %s\n", s.fail(err.Error()))
	}
}

func printSummary(w io.Writer, s style, report *BuildReport) {
	parts := []string{
		fmt.Sprintf("%d written", len(report.Written)),
		fmt.Sprintf("%d skipped", len(report.Skipped)),
		fmt.Sprintf("%d failed", len(report.Errors)),
		round(report.Duration),
	}
	line := strings.Join(parts, " · ")

	// Coloured by the worst thing in it, so the last line of the output says how
	// the build went without being read.
	switch {
	case len(report.Errors) > 0:
		line = s.fail(line)
	case len(report.Skipped) > 0:
		line = s.warn(line)
	default:
		line = s.ok(line)
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

// style writes colour and markers, or does not.
type style struct{ rich bool }

// newStyle decides whether w can take colour.
//
// A terminal, and not one that asked not to be coloured. NO_COLOR is honoured
// because it is the convention that exists, and TERM=dumb because a terminal saying
// it is dumb is a terminal saying escape codes will be shown rather than obeyed.
func newStyle(w io.Writer) style {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return style{}
	}
	file, ok := w.(*os.File)
	if !ok {
		return style{}
	}
	info, err := file.Stat()
	if err != nil {
		return style{}
	}
	return style{rich: info.Mode()&os.ModeCharDevice != 0}
}

// mark returns the symbol for a terminal, or the ASCII stand-in for everything
// else. They travel together deliberately: a destination that cannot be trusted
// with colour is one that may not render the symbol either, and one rule is easier
// to predict than two.
func (s style) mark(symbol, plain string) string {
	if s.rich {
		return symbol
	}
	return plain
}

func (s style) wrap(code, text string) string {
	if !s.rich {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s style) ok(text string) string   { return s.wrap("32", text) }
func (s style) warn(text string) string { return s.wrap("33", text) }
func (s style) fail(text string) string { return s.wrap("31", text) }
func (s style) bold(text string) string { return s.wrap("1", text) }
func (s style) dim(text string) string  { return s.wrap("2", text) }

// Package term decides whether output is going somewhere that can take colour, and
// writes it either way.
//
// One decision in one place, because the alternative is each caller inventing its
// own answer and the framework producing escape codes in a log file from one code
// path and not from another.
package term

import (
	"io"
	"os"
	"strings"
)

// Style writes colour and marker characters, or does not.
type Style struct{ rich bool }

// NewStyle decides whether w can take colour.
//
// A terminal, and not one that asked not to be coloured. NO_COLOR is honoured
// because it is the convention that exists, and TERM=dumb because a terminal saying
// it is dumb is a terminal saying escape codes will be shown rather than obeyed.
//
// Everything else — a pipe, a file, a CI log — is plain. Escape codes in a log
// outlive the session that wrote them and are noise in every reader that opens it
// afterwards. The exception is FORCE_COLOR, the convention for a pipe that ends
// at a terminal anyway: "collage dev" sets it for the program it runs, whose
// output it reads to show on the error page and passes on to the terminal it
// was started from. It applies to a file only, never an in-memory writer, and
// NO_COLOR still wins.
func NewStyle(w io.Writer) Style {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return Style{}
	}
	file, ok := w.(*os.File)
	if !ok {
		return Style{}
	}
	if force := os.Getenv("FORCE_COLOR"); force != "" && force != "0" && force != "false" {
		return Style{rich: true}
	}
	info, err := file.Stat()
	if err != nil {
		return Style{}
	}
	return Style{rich: info.Mode()&os.ModeCharDevice != 0}
}

// Rich reports whether this style writes colour.
func (s Style) Rich() bool { return s.rich }

// Mark returns symbol for a terminal, or plain for everything else.
//
// They travel together deliberately: a destination that cannot be trusted with
// colour is one that may not render the symbol either, and one rule is easier to
// predict than two.
func (s Style) Mark(symbol, plain string) string {
	if s.rich {
		return symbol
	}
	return plain
}

func (s Style) wrap(code, text string) string {
	if !s.rich {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// OK, Warn, Fail, Bold and Dim colour text, or return it unchanged.
func (s Style) OK(text string) string   { return s.wrap("32", text) }
func (s Style) Warn(text string) string { return s.wrap("33", text) }
func (s Style) Fail(text string) string { return s.wrap("31", text) }
func (s Style) Info(text string) string { return s.wrap("36", text) }
func (s Style) Bold(text string) string { return s.wrap("1", text) }
func (s Style) Dim(text string) string  { return s.wrap("2", text) }

// StripEscapes removes the colour escape codes Style writes from text, for
// output that was coloured for a terminal and is shown somewhere else.
func StripEscapes(text string) string {
	if !strings.Contains(text, "\x1b[") {
		return text
	}
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == 0x1b && i+1 < len(text) && text[i+1] == '[' {
			j := i + 2
			for j < len(text) && (text[j] == ';' || (text[j] >= '0' && text[j] <= '9')) {
				j++
			}
			if j < len(text) && text[j] == 'm' {
				i = j
				continue
			}
		}
		out.WriteByte(text[i])
	}
	return out.String()
}

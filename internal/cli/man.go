package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"
)

// manUsage is "collage help man"'s own usage text.
const manUsage = `Usage: collage man [--install]

Prints collage's man page, collage(1), made from every command's own help.

  --install   write it to ../share/man/man1/collage.1 beside the directory the
              collage binary is in — for "go install", $(go env GOPATH)/share/man
              — which man searches for every directory on PATH, so "man collage"
              finds it with nothing else to set up
`

// executable is os.Executable, a variable so a test can point man --install
// at a binary of its own.
var executable = os.Executable

// defineManFlags declares man's flags on fs: whether to install the page
// rather than print it. runMan parses them; completion reads the same
// declaration.
func defineManFlags(fs *flag.FlagSet) (install *bool) {
	return fs.Bool("install", false, "write the page where man finds it, beside the collage binary")
}

// runMan implements the "man" command.
func (c *CLI) runMan(args []string) int {
	fs := flag.NewFlagSet("man", flag.ContinueOnError)
	fs.SetOutput(c.stderr())
	fs.Usage = func() { fmt.Fprint(c.stderr(), manUsage) }
	install := defineManFlags(fs)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(c.stderr(), "collage: man takes no arguments")
		fmt.Fprintln(c.stderr())
		fs.Usage()
		return 2
	}
	date := buildDate()
	if date == "" {
		// A binary built from the module proxy records no commit date: the
		// page is dated the day it is written instead.
		date = time.Now().Format(time.DateOnly)
	}
	page := renderMan(c, Version, date)
	if !*install {
		_, _ = c.stdout().Write(page)
		return 0
	}
	path, onPath, err := manInstallPath()
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o755)
	}
	if err == nil {
		err = writeFileAtomic(path, page, 0o644)
	}
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: man --install: %v\n", err)
		return 1
	}
	fmt.Fprintf(c.stdout(), "Wrote %s\n", path)
	if !onPath {
		fmt.Fprintf(c.stdout(), "The collage binary's directory is not on PATH, so man will not look there: add %s to MANPATH, or put the binary on PATH.\n", filepath.Dir(filepath.Dir(path)))
	}
	return 0
}

// manInstallPath is where man --install writes: share/man/man1 beside the
// binary's own directory, the place man derives from PATH. onPath reports
// whether that directory is on PATH, without which man does not look.
func manInstallPath() (path string, onPath bool, err error) {
	exe, err := executable()
	if err != nil {
		return "", false, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", false, err
	}
	bin := filepath.Dir(exe)
	path = filepath.Join(filepath.Dir(bin), "share", "man", "man1", "collage.1")
	return path, slices.ContainsFunc(filepath.SplitList(os.Getenv("PATH")), func(dir string) bool {
		resolved, err := filepath.EvalSymlinks(dir)
		return err == nil && resolved == bin
	}), nil
}

// buildDate is the date of the commit this binary was built from, which is
// what the page describes; "" for a build that recorded none.
func buildDate() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.time" && len(s.Value) >= len("2006-01-02") {
			return s.Value[:len("2006-01-02")]
		}
	}
	return ""
}

// renderMan writes collage(1): a section per command holding that command's
// own help, as "collage help <command>" prints it, so the page cannot say
// something the help does not. version and date are passed in rather than
// read, so the same input renders the same page.
func renderMan(c *CLI, version, date string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, ".TH COLLAGE 1 %q %q \"Collage Manual\"\n", date, "collage "+version)
	b.WriteString(".SH NAME\ncollage \\- the CLI for collage, a Go framework for server-side component-based rendering\n")
	b.WriteString(".SH SYNOPSIS\n.B collage\n.I command\n.RI [ flags ]\n")
	b.WriteString(".SH DESCRIPTION\n")
	b.WriteString("collage scaffolds a project, writes its pages, fragments and actions, adds its plugins, runs it in development, builds the binary to deploy and exports it to static files.\n")
	b.WriteString("Each command is described below, as\n.B collage help\n.I command\nprints it.\n")
	b.WriteString(".SH COMMANDS\n")
	for _, cmd := range builtins {
		var help bytes.Buffer
		if err := c.printCommandHelp(&help, cmd.name); err != nil {
			continue
		}
		fmt.Fprintf(&b, ".SS %s\n%s\n.PP\n.nf\n", cmd.name, roffText(cmd.short))
		for line := range strings.SplitSeq(strings.TrimRight(help.String(), "\n"), "\n") {
			b.WriteString(roffText(line))
			b.WriteByte('\n')
		}
		b.WriteString(".fi\n")
	}
	b.WriteString(".SH EXIT STATUS\n.TP\n0\nSuccess.\n.TP\n1\nThe command parsed but failed to do its work.\n.TP\n2\nA usage error: no command, an unknown one, or wrong arguments.\n")
	b.WriteString(".SH SEE ALSO\nhttps://collage.furkanbaytekin.dev/en/docs/introduction/\n")
	return b.Bytes()
}

// roffText escapes one line of text for roff: a backslash and a dash as roff
// writes them, a line starting with a control character made text, and
// anything beyond ASCII written as its code point, which every man reads.
func roffText(line string) string {
	var b strings.Builder
	if strings.HasPrefix(line, ".") || strings.HasPrefix(line, "'") {
		b.WriteString(`\&`)
	}
	for _, r := range line {
		switch {
		case r == '\\':
			b.WriteString(`\e`)
		case r == '-':
			b.WriteString(`\-`)
		case r == '—':
			b.WriteString(`\(em`)
		case r > 0x7f:
			fmt.Fprintf(&b, `\[u%04X]`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Elagoht/collage/internal/suggest"
)

// didYouMean is what an error rejecting name appends when one of candidates is
// close to it: ` (did you mean "check"?)`, worded as the template checker
// words its own. It is "" when none is, so a line naming something nowhere
// near a valid name reads as it always has.
func didYouMean(name string, candidates []string) string {
	return prefixedDidYouMean("", name, candidates)
}

// prefixedDidYouMean is didYouMean for names written with prefix before them,
// a flag's dash or a bundle's @: the names are compared without it, so the
// prefix neither counts as a letter nor moves a short name into the range
// where two edits are allowed, and the suggestion is written with it.
func prefixedDidYouMean(prefix, name string, candidates []string) string {
	s := suggest.Closest(name, candidates)
	if s == "" || s == name {
		return ""
	}
	return fmt.Sprintf(" (did you mean %q?)", prefix+s)
}

// undefinedFlag is the start of the flag package's message for a flag the
// FlagSet does not define; the flag's name follows it, after one dash.
const undefinedFlag = "flag provided but not defined: -"

// parseFlags is fs.Parse(args), except that a flag fs does not define is
// answered with the closest one it does, on the flag package's own line.
// Every command parses its flags through it, so they all answer alike.
//
// The flag package writes that line and then calls fs.Usage itself, before
// Parse returns, so nothing added afterwards could reach the line: it would
// land below the usage text. parseFlags silences both for the call and writes
// them again once it knows the error — the same line, the same usage, in the
// same order — so only the suggestion is new.
func parseFlags(fs *flag.FlagSet, args []string) error {
	out, usage := fs.Output(), fs.Usage
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	err := fs.Parse(args)
	fs.SetOutput(out)
	fs.Usage = usage
	if err == nil {
		return nil
	}
	if !errors.Is(err, flag.ErrHelp) {
		// -h prints the usage alone; every other error is a line first.
		fmt.Fprintln(out, err.Error()+flagSuggestion(fs, err))
	}
	if usage != nil {
		usage()
	} else {
		// The flag package's own default, which it does not export.
		fmt.Fprintf(out, "Usage of %s:\n", fs.Name())
		fs.PrintDefaults()
	}
	return err
}

// flagSuggestion is didYouMean for err when it rejects a flag fs does not
// define, with one dash before the suggestion as the flag package puts one
// before the name it rejects; "" for any other error.
func flagSuggestion(fs *flag.FlagSet, err error) string {
	name, ok := strings.CutPrefix(err.Error(), undefinedFlag)
	if !ok {
		return ""
	}
	var names []string
	fs.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	return prefixedDidYouMean("-", name, names)
}

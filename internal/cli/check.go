package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
)

const checkUsage = `Usage: collage check [-json]

Checks the current directory's project without rendering it, and prints what it
finds, one line each: a template linking by name — {{pageURL "post"}},
{{actionURL "logout"}}, {{fragmentURL "home" "clock"}} — to a page, document,
action or fragment path that is not registered, into a locale no URL reaches or
the route has no path in, or with parameters its pattern does not take. Each of
those fails when the template renders; check finds them in every template at
once. Only names written as string literals are checked.

  -json   print the findings as a JSON array, for an editor

Exits 1 when it finds anything, so it can stand in CI before a build.

Builds the current directory's Go project as collage dev does — with the
collage_dev tag, so nothing is embedded — and runs it as "<program>
collage-check" with COLLAGE_DEV=1, so it reads templates/ and static/ from
disk. The scaffolded main.go hands the word after its flags to
collage.DispatchCommands, which answers collage-check itself, after starting
the application — so a program that opens a database on start opens it here
too. It needs the project's collage at v0.40.0 or later; an earlier one
answers unknown command: "collage-check".
`

// defineCheckFlags declares check's one flag on fs. check reads it by hand,
// since it hands it on to the program rather than acting on it; completion and
// the man page read this declaration.
func defineCheckFlags(fs *flag.FlagSet) *bool {
	return fs.Bool("json", false, "print the findings as a JSON array, for an editor")
}

// runCheck implements the "check" command.
func (c *CLI) runCheck(ctx context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
		fmt.Fprint(c.stdout(), checkUsage)
		return 0
	}
	command := []string{"collage-check"}
	switch {
	case len(args) == 1 && (args[0] == "-json" || args[0] == "--json"):
		command = append(command, "-json")
	case len(args) != 0:
		hint := ""
		if name, ok := strings.CutPrefix(args[0], "-"); ok && len(args) == 1 {
			// check reads its one flag by hand, so it suggests it by hand.
			hint = prefixedDidYouMean("-", strings.TrimPrefix(name, "-"), []string{"json"})
		}
		fmt.Fprintln(c.stderr(), "collage: check takes no arguments but -json"+hint)
		fmt.Fprintln(c.stderr())
		fmt.Fprint(c.stderr(), checkUsage)
		return 2
	}
	if err := c.runDevProgram(ctx, c.stdout(), c.stderr(), command...); err != nil {
		// The program has already said what it found; this is only the exit.
		return 1
	}
	return 0
}

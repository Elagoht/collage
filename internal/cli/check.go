package cli

import (
	"context"
	"fmt"
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

Runs "go run . collage-check" in the current directory's Go project. The
scaffolded main.go hands the word after its flags to collage.DispatchCommands,
which answers collage-check itself, after starting the application — so a
program that opens a database on start opens it here too.
`

// runCheck implements the "check" command.
func (c *CLI) runCheck(ctx context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
		fmt.Fprint(c.stdout(), checkUsage)
		return 0
	}
	command := []string{"run", ".", "collage-check"}
	switch {
	case len(args) == 1 && (args[0] == "-json" || args[0] == "--json"):
		command = append(command, "-json")
	case len(args) != 0:
		fmt.Fprintln(c.stderr(), "collage: check takes no arguments but -json")
		fmt.Fprintln(c.stderr())
		fmt.Fprint(c.stderr(), checkUsage)
		return 2
	}
	if err := c.runner().Run(ctx, "", nil, c.stdout(), c.stderr(), "go", command...); err != nil {
		// The program has already said what it found; this is only the exit.
		return 1
	}
	return 0
}

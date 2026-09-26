package cli

import (
	"context"
	"fmt"
)

const inspectUsage = `Usage: collage inspect

Prints what the current directory's project is made of, as JSON: every page,
fragment, document and action by name, the slots each template fills, the
template functions, the plugins, the locales and the files the mounts serve.
It is what an editor's completion reads; the Collage Snippets & Highlighter
extension for VS Code runs it.

Runs "go run . collage-inspect" in the current directory's Go project. The
scaffolded main.go hands the word after its flags to collage.DispatchCommands,
which answers collage-inspect itself; a program that does not dispatch its
arguments that way has nothing to answer with.
`

// runInspect implements the "inspect" command.
func (c *CLI) runInspect(ctx context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
		fmt.Fprint(c.stdout(), inspectUsage)
		return 0
	}
	if len(args) != 0 {
		fmt.Fprintln(c.stderr(), "collage: inspect takes no arguments")
		fmt.Fprintln(c.stderr())
		fmt.Fprint(c.stderr(), inspectUsage)
		return 2
	}
	if err := c.runner().Run(ctx, "", nil, c.stdout(), c.stderr(), "go", "run", ".", "collage-inspect"); err != nil {
		fmt.Fprintf(c.stderr(), "collage: inspect: %v\n", err)
		return 1
	}
	return 0
}

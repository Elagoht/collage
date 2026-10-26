package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// aiUsage is "collage help ai"'s own usage text.
const aiUsage = `Usage: collage ai init [--dir path]

Writes AGENTS.md into the project: the rules a coding agent (opencode, Codex,
Cursor, Claude Code…) needs to work in a collage project — where each kind of
file goes, what a layout change reaches, guards, and which plugin to add instead
of writing one. Run it again after upgrading collage to refresh them.

collage writes only its own section, between the collage:begin and collage:end
markers; anything else in AGENTS.md is the project's own and is kept as it is.
An AGENTS.md without the markers gets the section above its text.

A CLAUDE.md that imports AGENTS.md is written too when the project has none, so
Claude Code reads the same rules. An existing CLAUDE.md is never changed.

Flags:
  --dir path   the project (default ".")
`

// claudeImport is the CLAUDE.md "ai init" writes: Claude Code reads CLAUDE.md
// rather than AGENTS.md, and this line makes it read AGENTS.md as well.
const claudeImport = "@AGENTS.md\n"

// aiWritten says what writeAIFiles did with each file: "created", "updated",
// "current" (already what it would write), or for CLAUDE.md "exists" (left
// alone because the project has its own).
type aiWritten struct {
	Agents, Claude string
}

// writeAIFiles writes dir's AGENTS.md with version's section, keeping
// everything outside its markers, and a CLAUDE.md importing it when dir has
// none. Nothing is written when AGENTS.md's markers are malformed.
func writeAIFiles(dir, version string) (aiWritten, error) {
	var done aiWritten
	// A symlinked AGENTS.md — rules shared between projects — is written
	// through, to its target and with the target's mode, rather than replaced
	// by a file of its own.
	agentsPath, perm := filepath.Join(dir, "AGENTS.md"), fs.FileMode(0o644)
	existing, err := os.ReadFile(agentsPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		existing = nil
	case err != nil:
		return done, err
	default:
		if agentsPath, err = filepath.EvalSymlinks(agentsPath); err != nil {
			return done, err
		}
		info, err := os.Stat(agentsPath)
		if err != nil {
			return done, err
		}
		perm = info.Mode().Perm()
	}
	merged, err := mergeAgents(existing, managedSection(version))
	if err != nil {
		return done, err
	}
	switch {
	case existing == nil:
		done.Agents = "created"
	case bytes.Equal(existing, merged):
		done.Agents = "current"
	default:
		done.Agents = "updated"
	}
	if done.Agents != "current" {
		if err := writeFileAtomic(agentsPath, merged, perm); err != nil {
			return done, err
		}
	}

	claudePath := filepath.Join(dir, "CLAUDE.md")
	switch _, err := os.Lstat(claudePath); {
	case err == nil:
		done.Claude = "exists"
	case errors.Is(err, fs.ErrNotExist):
		if err := writeFileAtomic(claudePath, []byte(claudeImport), 0o644); err != nil {
			return done, err
		}
		done.Claude = "created"
	default:
		return done, err
	}
	return done, nil
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory, renamed over path once complete, so an interrupted write leaves
// the old file rather than half of the new one.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(name, perm)
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

// runAI implements the "ai" command; "init" is its one subcommand.
func (c *CLI) runAI(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(c.stderr(), "collage: ai: want a subcommand: init")
		fmt.Fprintln(c.stderr())
		fmt.Fprint(c.stderr(), aiUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		fmt.Fprint(c.stdout(), aiUsage)
		return 0
	case "init":
		return c.runAIInit(args[1:])
	}
	fmt.Fprintf(c.stderr(), "collage: ai: unknown subcommand %q\n\n", args[0])
	fmt.Fprint(c.stderr(), aiUsage)
	return 2
}

// runAIInit implements "ai init".
func (c *CLI) runAIInit(args []string) int {
	flags := flag.NewFlagSet("ai init", flag.ContinueOnError)
	flags.SetOutput(c.stderr())
	flags.Usage = func() { fmt.Fprint(c.stderr(), aiUsage) }
	dir := flags.String("dir", ".", "the project")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(c.stderr(), "collage: ai init: unexpected argument %q\n\n", flags.Arg(0))
		flags.Usage()
		return 2
	}
	proj, err := readProject(*dir)
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: ai init: %v\n", err)
		return 1
	}
	done, err := writeAIFiles(proj.dir, Version)
	if err != nil {
		fmt.Fprintf(c.stderr(), "collage: ai init: %v\n", err)
		return 1
	}
	c.printAIWritten(done, proj.dir)
	return 0
}

// printAIWritten reports what writeAIFiles did, and says so when an existing
// CLAUDE.md does not import AGENTS.md: Claude Code would not read the rules.
func (c *CLI) printAIWritten(done aiWritten, dir string) {
	out := c.stdout()
	fmt.Fprintf(out, "AGENTS.md: %s\n", done.Agents)
	fmt.Fprintf(out, "CLAUDE.md: %s\n", done.Claude)
	if done.Claude != "exists" {
		return
	}
	claude, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err == nil && !bytes.Contains(claude, []byte("@AGENTS.md")) {
		fmt.Fprintln(out, `CLAUDE.md does not import AGENTS.md; add a line "@AGENTS.md" to it`)
	}
}

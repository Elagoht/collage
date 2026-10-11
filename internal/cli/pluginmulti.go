package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Elagoht/collage/internal/cli/plugincontract"
)

// notAddedError is "plugin del"'s refusal of a plugin plugins.go does not
// have; for a plugin only a bundle named, it is a plugin to pass over.
type notAddedError struct {
	name string
	have string // what plugins.go has, for the message
}

func (e *notAddedError) Error() string {
	return fmt.Sprintf("%s is not in plugins.go; it has: %s", e.name, e.have)
}

// requiredError is "plugin del"'s refusal of a plugin another one requires;
// when that one is being removed too, the refused one is tried again after.
type requiredError struct {
	by, target string
}

func (e *requiredError) Error() string {
	return fmt.Sprintf("%s requires %s; remove it first", e.by, e.target)
}

// labelled is err named for r, unless its message already starts with the
// plugin's name or path, as a word of its own.
func labelled(r namedRef, err error) string {
	msg := err.Error()
	for _, prefix := range []string{r.label, r.ref.Path} {
		if prefix == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(msg, prefix); ok && (rest == "" || strings.ContainsAny(rest[:1], ": @")) {
			return msg
		}
	}
	return r.label + ": " + msg
}

// addPlugins adds refs to the project at dir, one by one, each in a step of
// its own whose files are put back when it fails, and returns the exit code,
// 1 when any failed, and whether any was added. With several, the setup notes
// are printed together at the end, after a summary.
func (c *CLI) addPlugins(ctx context.Context, dir string, refs []namedRef) (int, bool) {
	fail := func(err error) (int, bool) {
		fmt.Fprintf(c.stderr(), "collage: plugin add: %v\n", err)
		return 1, false
	}
	if err := checkPackageMain(dir); err != nil {
		return fail(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return fail(fmt.Errorf("no go.mod in %s", dir))
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	multi := len(refs) > 1
	var added, failed, untried []string
	var notes []addedPlugin
	for i, r := range refs {
		if ctx.Err() != nil {
			for _, rest := range refs[i:] {
				untried = append(untried, rest.label)
			}
			break
		}
		got, err := c.addOne(ctx, dir, r.ref)
		if err != nil {
			if multi {
				fmt.Fprintf(c.stderr(), "collage: plugin add: %s\n", labelled(r, err))
			} else {
				fmt.Fprintf(c.stderr(), "collage: plugin add: %v\n", err)
			}
			failed = append(failed, r.label)
			continue
		}
		added = append(added, r.label)
		if multi {
			for j := range got {
				if got[j].Setup != "" {
					notes = append(notes, got[j])
					got[j].Setup = ""
				}
			}
			if len(added) > 1 {
				fmt.Fprintln(c.stdout())
			}
		}
		c.printAdded(got)
	}

	if multi {
		c.printSummary([][2]string{{"added", strings.Join(added, ", ")}, {"failed", strings.Join(failed, ", ")}, {"not tried", strings.Join(untried, ", ")}})
		if len(notes) > 0 {
			w := c.stdout()
			fmt.Fprintln(w)
			fmt.Fprintln(w, "To finish setting up:")
			for _, n := range notes {
				fmt.Fprintln(w)
				fmt.Fprintln(w, n.Name)
				for _, line := range strings.Split(strings.TrimSpace(printable(n.Setup)), "\n") {
					fmt.Fprintln(w, "  "+line)
				}
			}
		}
	}
	if len(added) > 0 && !callsPlugins(dir) {
		fmt.Fprintln(c.stdout())
		fmt.Fprintln(c.stdout(), "plugins.go is new: give it to collage.New once —")
		fmt.Fprintln(c.stdout(), "\tPlugins: plugins(),")
	}
	if !multi && len(untried) > 0 {
		fmt.Fprintf(c.stderr(), "collage: plugin add: %s: interrupted\n", untried[0])
	}
	if len(failed) > 0 || len(untried) > 0 {
		return 1, len(added) > 0
	}
	return 0, len(added) > 0
}

// addOne adds ref and the plugins it requires, then tidies go.mod; the files
// it touches are put back as they were when it fails, is interrupted, or
// panics.
func (c *CLI) addOne(ctx context.Context, dir string, ref plugincontract.Ref) ([]addedPlugin, error) {
	snap, err := snapshot(
		filepath.Join(dir, "go.mod"),
		filepath.Join(dir, "go.sum"),
		filepath.Join(dir, "plugins.go"),
		filepath.Join(dir, "plugins-config.json"),
	)
	if err != nil {
		return nil, err
	}
	guard, commit := c.restoreUnlessCommitted("plugin add", snap)
	defer guard()
	added, err := c.pluginAdd(ctx, dir, ref, map[string]bool{})
	if err == nil {
		err = c.goTidy(ctx, dir)
	}
	if err != nil {
		return nil, err
	}
	commit()
	return added, nil
}

// delPlugins removes refs from the project at dir, one by one, each in a step
// of its own, and returns the exit code: 1 when any failed. A plugin refused
// because another one requires it is tried again once a pass removed
// something, so the order refs come in does not matter; a bundle's plugin
// that is not there is passed over.
func (c *CLI) delPlugins(ctx context.Context, dir string, refs []namedRef) int {
	fail := func(err error) int {
		fmt.Fprintf(c.stderr(), "collage: plugin del: %v\n", err)
		return 1
	}
	if err := checkPackageMain(dir); err != nil {
		return fail(err)
	}
	if src, err := readIfExists(filepath.Join(dir, "plugins.go")); err != nil {
		return fail(err)
	} else if src == nil {
		return fail(fmt.Errorf("no plugins.go in %s; nothing to remove", dir))
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	multi := len(refs) > 1
	report := func(r namedRef, err error) {
		if multi {
			fmt.Fprintf(c.stderr(), "collage: plugin del: %s\n", labelled(r, err))
		} else {
			fmt.Fprintf(c.stderr(), "collage: plugin del: %v\n", err)
		}
	}
	var removed, failed, absent, untried []string
	pending := refs
	for len(pending) > 0 {
		type refused struct {
			r   namedRef
			err error
		}
		var again []refused
		progress := false
		for i, r := range pending {
			if ctx.Err() != nil {
				for _, rest := range pending[i:] {
					untried = append(untried, rest.label)
				}
				for _, a := range again {
					untried = append(untried, a.r.label)
				}
				again, pending = nil, nil
				break
			}
			name, settings, err := c.delOne(ctx, dir, r.ref)
			var required *requiredError
			var notAdded *notAddedError
			switch {
			case err == nil:
				if multi && len(removed) > 0 {
					fmt.Fprintln(c.stdout())
				}
				fmt.Fprintf(c.stdout(), "removed %s\n", name)
				if settings != "" {
					fmt.Fprintln(c.stdout(), "its settings were:")
					fmt.Fprintln(c.stdout(), settings)
				}
				removed = append(removed, name)
				progress = true
			case errors.As(err, &notAdded) && r.fromBundle:
				absent = append(absent, r.label)
			case errors.As(err, &required) && multi:
				again = append(again, refused{r, err})
			default:
				report(r, err)
				failed = append(failed, r.label)
			}
		}
		pending = nil
		for _, a := range again {
			if progress {
				pending = append(pending, a.r)
				continue
			}
			report(a.r, a.err)
			failed = append(failed, a.r.label)
		}
	}

	if !multi && len(untried) > 0 {
		fmt.Fprintf(c.stderr(), "collage: plugin del: %s: interrupted\n", untried[0])
	}
	if multi {
		c.printSummary([][2]string{{"removed", strings.Join(removed, ", ")}, {"failed", strings.Join(failed, ", ")}, {"not added", strings.Join(absent, ", ")}, {"not tried", strings.Join(untried, ", ")}})
	}
	if len(failed) > 0 || len(untried) > 0 {
		return 1
	}
	return 0
}

// delOne removes ref, then tidies go.mod; the files it touches are put back
// as they were when it fails, is interrupted, or panics.
func (c *CLI) delOne(ctx context.Context, dir string, ref plugincontract.Ref) (string, string, error) {
	pluginsPath := filepath.Join(dir, "plugins.go")
	src, err := readIfExists(pluginsPath)
	if err != nil {
		return "", "", err
	}
	snap, err := snapshot(
		filepath.Join(dir, "go.mod"),
		filepath.Join(dir, "go.sum"),
		pluginsPath,
		filepath.Join(dir, "plugins-config.json"),
	)
	if err != nil {
		return "", "", err
	}
	guard, commit := c.restoreUnlessCommitted("plugin del", snap)
	defer guard()
	name, settings, err := c.pluginDel(ctx, dir, ref, src)
	if err == nil {
		err = c.goTidy(ctx, dir)
	}
	if err != nil {
		return "", "", err
	}
	commit()
	return name, settings, nil
}

// printSummary writes the lines of a summary whose value is not empty, after
// a blank line.
func (c *CLI) printSummary(lines [][2]string) {
	w := c.stdout()
	fmt.Fprintln(w)
	for _, l := range lines {
		if l[1] != "" {
			fmt.Fprintf(w, "%s: %s\n", l[0], l[1])
		}
	}
}

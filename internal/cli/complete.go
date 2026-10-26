package cli

import (
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
)

// completeCommand is the hidden command the shell scripts "collage completion"
// prints call on every TAB. It is reserved like a built-in but listed nowhere:
// it is the scripts' interface, not a person's.
const completeCommand = "__complete"

// filesDirective is the line completeCommand ends with when the word being
// completed is a path: the shell's own file completion knows the disk better
// than collage does.
const filesDirective = ":files"

// commandSpec is what completion knows about one command: the flags it
// declares and what its positional arguments are.
type commandSpec struct {
	// flags declares the command's flags on a FlagSet, through the same
	// define function the command parses with; nil for a command with none.
	flags func(*flag.FlagSet)
	// subcommands are the words that may follow the command, for one that
	// dispatches on them; each is its own entry, "plugin add".
	subcommands []string
	// positional returns the candidates for the i-th argument that is not a
	// flag, and whether it is a path; nil for a command that takes none.
	positional func(i int) (candidates []string, files bool)
}

// commandSpecs are every built-in command and subcommand. A test keeps it in
// step with builtins.
var commandSpecs = map[string]commandSpec{
	"new":        {flags: func(fs *flag.FlagSet) { defineNewFlags(fs) }},
	"add":        {flags: func(fs *flag.FlagSet) { defineAddFlags(fs) }, positional: func(i int) ([]string, bool) { return firstOnly(i, addKinds) }},
	"plugin":     {subcommands: []string{"add", "del", "check"}},
	"plugin add": {flags: func(fs *flag.FlagSet) { definePluginDirFlag(fs) }, positional: func(int) ([]string, bool) { return pluginCandidates(), false }},
	"plugin del": {flags: func(fs *flag.FlagSet) { definePluginDirFlag(fs) }, positional: func(int) ([]string, bool) { return pluginCandidates(), false }},
	"plugin check": {flags: func(fs *flag.FlagSet) { definePluginCheckFlags(fs) }, positional: func(i int) ([]string, bool) {
		return nil, i == 0
	}},
	"ai":         {subcommands: []string{"init"}},
	"ai init":    {flags: func(fs *flag.FlagSet) { defineAIInitFlags(fs) }},
	"dev":        {},
	"build":      {flags: func(fs *flag.FlagSet) { defineBuildFlags(fs) }},
	"export":     {flags: func(fs *flag.FlagSet) { defineExportFlags(fs) }},
	"serve":      {flags: func(fs *flag.FlagSet) { defineServeFlags(fs) }},
	"routes":     {},
	"inspect":    {},
	"check":      {flags: func(fs *flag.FlagSet) { defineCheckFlags(fs) }},
	"completion": {positional: func(i int) ([]string, bool) { return firstOnly(i, completionShells) }},
	"man":        {flags: func(fs *flag.FlagSet) { defineManFlags(fs) }},
	"version":    {},
	"help":       {positional: func(i int) ([]string, bool) { return firstOnly(i, builtinNames()) }},
}

// pathFlags are the flags, by name, whose value is a file or a directory.
var pathFlags = map[string]bool{"dir": true, "o": true, "out": true}

// flagValues are the values a flag takes from a fixed set, by flag name.
var flagValues = map[string][]string{
	"template": {variantMinimal, variantDemo},
	"os":       {"linux", "darwin", "windows", "freebsd", "openbsd", "netbsd"},
	"arch":     {"amd64", "arm64", "386", "arm", "riscv64"},
}

// firstOnly offers candidates for the first positional argument and nothing
// after it.
func firstOnly(i int, candidates []string) ([]string, bool) {
	if i == 0 {
		return candidates, false
	}
	return nil, false
}

// builtinNames are the listed commands' names, in the order help lists them.
func builtinNames() []string {
	names := make([]string, 0, len(builtins))
	for _, b := range builtins {
		names = append(names, b.name)
	}
	return names
}

// pluginCandidates are what "plugin add" and "plugin del" complete: every
// bundle, then every plugin the bundles name.
func pluginCandidates() []string {
	var out []string
	for _, b := range bundles {
		out = append(out, "@"+b.Name)
	}
	return append(out, bundledPlugins()...)
}

// bundledPlugins are the plugins the bundles name, each once, sorted.
func bundledPlugins() []string {
	var out []string
	for _, b := range bundles {
		for _, p := range b.Plugins {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	slices.Sort(out)
	return out
}

// specFlags returns the FlagSet the command at path declares, never parsed.
func specFlags(path string) *flag.FlagSet {
	fs := flag.NewFlagSet(path, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if spec, ok := commandSpecs[path]; ok && spec.flags != nil {
		spec.flags(fs)
	}
	return fs
}

// isBoolFlag reports whether f takes no value, as the flag package decides it.
func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// complete returns the candidates for the last of args, the word being
// completed — "" when the cursor follows a space — given the words before it,
// and whether the shell should complete paths instead. It filters by the
// current word itself, so a shell can show what it returns as it is.
func complete(args []string) (candidates []string, files bool) {
	if len(args) == 0 {
		return nil, false
	}
	words, cur := args[:len(args)-1], args[len(args)-1]
	if len(words) == 0 {
		return withPrefix(builtinNames(), cur), false
	}
	path, rest := words[0], words[1:]
	spec, ok := commandSpecs[path]
	if !ok {
		return nil, false
	}
	if spec.subcommands != nil {
		if len(rest) == 0 {
			return withPrefix(spec.subcommands, cur), false
		}
		path, rest = path+" "+rest[0], rest[1:]
		if spec, ok = commandSpecs[path]; !ok {
			return nil, false
		}
	}
	fs := specFlags(path)

	// A flag's value: "--template=de" in one word, as zsh and fish pass it;
	// "--template" "=" "de", as bash splits it at its word breaks; or the word
	// after a flag that takes one.
	if name, value, ok := strings.Cut(cur, "="); ok && strings.HasPrefix(name, "-") {
		values, files := flagValueCandidates(fs, strings.TrimLeft(name, "-"), value)
		return prefixEach(name+"=", values), files
	}
	if n := len(rest); n >= 2 && rest[n-1] == "=" && strings.HasPrefix(rest[n-2], "-") {
		return flagValueCandidates(fs, strings.TrimLeft(rest[n-2], "-"), cur)
	}
	if n := len(rest); n >= 1 && strings.HasPrefix(rest[n-1], "-") && !strings.Contains(rest[n-1], "=") {
		if f := fs.Lookup(strings.TrimLeft(rest[n-1], "-")); f != nil && !isBoolFlag(f) {
			return flagValueCandidates(fs, f.Name, cur)
		}
	}

	if strings.HasPrefix(cur, "-") {
		dashes := "--"
		if !strings.HasPrefix(cur, "--") {
			dashes = "-"
		}
		var names []string
		fs.VisitAll(func(f *flag.Flag) { names = append(names, dashes+f.Name) })
		return withPrefix(names, cur), false
	}
	if spec.positional == nil {
		return nil, false
	}
	candidates, files = spec.positional(positionalIndex(fs, rest))
	return withPrefix(candidates, cur), files
}

// positionalIndex counts the words of rest that are positional arguments:
// neither a flag nor the value a flag before it takes.
func positionalIndex(fs *flag.FlagSet, rest []string) int {
	n := 0
	for i := 0; i < len(rest); i++ {
		word := rest[i]
		if !strings.HasPrefix(word, "-") || word == "-" {
			n++
			continue
		}
		if strings.Contains(word, "=") {
			continue
		}
		if f := fs.Lookup(strings.TrimLeft(word, "-")); f != nil && !isBoolFlag(f) {
			i++ // its value
		}
	}
	return n
}

// flagValueCandidates are the candidates for flag name's value, value being
// what is typed of it so far.
func flagValueCandidates(fs *flag.FlagSet, name, value string) ([]string, bool) {
	f := fs.Lookup(name)
	switch {
	case f == nil:
		return nil, false
	case pathFlags[name]:
		return nil, true
	case name == "with":
		// A comma list: complete its last item, keeping the ones before it, since
		// the shell matches the candidates against the whole word.
		done, last := "", value
		if i := strings.LastIndex(value, ","); i >= 0 {
			done, last = value[:i+1], value[i+1:]
		}
		return prefixEach(done, withPrefix(pluginCandidates(), last)), false
	}
	return withPrefix(flagValues[name], value), false
}

// withPrefix is candidates that start with prefix, in their order.
func withPrefix(candidates []string, prefix string) []string {
	var out []string
	for _, c := range candidates {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// prefixEach is candidates with prefix before each.
func prefixEach(prefix string, candidates []string) []string {
	if prefix == "" {
		return candidates
	}
	out := make([]string, len(candidates))
	for i, c := range candidates {
		out[i] = prefix + c
	}
	return out
}

// runComplete implements completeCommand: the candidates one a line, then the
// files directive when the shell should complete paths. It writes nothing to
// stderr and always succeeds, so a TAB never prints an error into the prompt.
func (c *CLI) runComplete(args []string) int {
	candidates, files := complete(args)
	out := c.stdout()
	for _, candidate := range candidates {
		fmt.Fprintln(out, candidate)
	}
	if files {
		fmt.Fprintln(out, filesDirective)
	}
	return 0
}

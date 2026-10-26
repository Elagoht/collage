package cli

import (
	"fmt"
	"slices"
)

// completionShells are the shells "collage completion" writes a script for.
var completionShells = []string{"bash", "zsh", "fish"}

// completionUsage is "collage help completion"'s own usage text.
const completionUsage = `Usage: collage completion bash|zsh|fish

Prints a script that completes collage's commands, flags and their values —
add's kinds, plugin add's bundles and plugins, --template's templates, paths for
--dir — when TAB is pressed. Load it from the shell's startup file:

  bash   eval "$(collage completion bash)"      in ~/.bashrc
  zsh    source <(collage completion zsh)       in ~/.zshrc, after compinit
  fish   collage completion fish | source       in ~/.config/fish/config.fish

The script asks the collage on PATH what to complete, so it stays right as
collage is upgraded. Commands a project's plugins add are not completed.
`

// completionScripts are the scripts, by shell. Each calls "collage __complete"
// with the words before the cursor and the word under it, and hands the shell
// the candidates, or its own file completion on the files directive.
var completionScripts = map[string]string{
	// Written for bash 3.2, macOS's own: no compopt, mapfile or readarray, and
	// eval rather than source <(…), which 3.2 ignores.
	"bash": `# collage completion for bash
_collage() {
    local cur="${COMP_WORDS[COMP_CWORD]}"
    local out c files=0
    # Before IFS changes: bash 3.2 joins a quoted array slice with IFS.
    out=$(collage __complete "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null)
    local IFS=$'\n'
    COMPREPLY=()
    for c in $out; do
        if [ "$c" = ":files" ]; then
            files=1
        else
            COMPREPLY+=("$c")
        fi
    done
    if [ "$files" = 1 ]; then
        COMPREPLY=($(compgen -f -- "$cur"))
    fi
}
complete -o filenames -F _collage collage
`,
	"zsh": `#compdef collage
# collage completion for zsh
_collage() {
    local -a lines
    lines=("${(@f)$(collage __complete "${(@)words[2,CURRENT]}" 2>/dev/null)}")
    if (( ${lines[(Ie):files]} )); then
        _files
        return
    fi
    lines=(${lines:#})
    (( ${#lines} )) && compadd -Q -- "${lines[@]}"
}
if (( $+functions[compdef] )); then
    compdef _collage collage
fi
`,
	"fish": `# collage completion for fish
function __collage_complete
    set -l words (commandline -opc)
    set -l out (collage __complete $words[2..-1] (commandline -ct) 2>/dev/null)
    if contains -- :files $out
        __fish_complete_path (commandline -ct)
        return
    end
    printf '%s\n' $out
end
complete -c collage -f -a '(__collage_complete)'
`,
}

// runCompletion implements the "completion" command.
func (c *CLI) runCompletion(args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
		fmt.Fprint(c.stdout(), completionUsage)
		return 0
	}
	if len(args) != 1 || !slices.Contains(completionShells, args[0]) {
		hint := ""
		if len(args) == 1 {
			hint = didYouMean(args[0], completionShells)
		}
		fmt.Fprintf(c.stderr(), "collage: completion: want one shell: bash, zsh or fish%s\n\n", hint)
		fmt.Fprint(c.stderr(), completionUsage)
		return 2
	}
	fmt.Fprint(c.stdout(), completionScripts[args[0]])
	return 0
}

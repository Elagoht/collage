package cli

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// scaffoldFS embeds the tree "collage new" scaffolds a project from. "all:" is
// used even though nothing under scaffold/ currently has a leading "." or "_"
// in its name, so a future addition to the tree does not silently go missing
// from the embedded copy.
//
//go:embed all:scaffold
var scaffoldFS embed.FS

// scaffoldRoot is the directory inside scaffoldFS the scaffold tree lives
// under. It holds one layer every project gets, "common" — main.go and its CLI
// contract, go.mod, the layout fragment and the home page's route — and one
// directory per template: "demo", the default, and "minimal".
const scaffoldRoot = "scaffold"

// Scaffold templates, the layer written over "common" — what "collage new
// --template" names.
const (
	variantDemo    = "demo"
	variantMinimal = "minimal"
)

// writeScaffold writes the embedded scaffold into targetDir — the common layer,
// then variant's over it — substituting module and name into every file (see
// substitute) and mapping each embedded path to the name it is written under
// (see targetName).
//
// Two layers rather than two copies, so what every project shares — main.go and
// its CLI contract, the layout fragment, the home page's route — exists once and
// cannot drift between them.
func writeScaffold(targetDir, module, name, variant string) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	for _, layer := range []string{"common", variant} {
		if err := writeLayer(targetDir, scaffoldRoot+"/"+layer, module, name); err != nil {
			return err
		}
	}
	return nil
}

// writeLayer writes one embedded scaffold layer, rooted at root, into targetDir.
func writeLayer(targetDir, root, module, name string) error {
	prefix := root + "/"

	return fs.WalkDir(scaffoldFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}

		// fs.WalkDir paths, like every io/fs path, always use "/" regardless
		// of GOOS; only the target written to the real filesystem needs
		// filepath's OS-specific separator.
		rel := strings.TrimPrefix(path, prefix)
		target := filepath.Join(targetDir, filepath.FromSlash(targetName(rel)))

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		content, err := scaffoldFS.ReadFile(path)
		if err != nil {
			return err
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, substitute(content, module, name), 0o644)
	})
}

// targetName maps an embedded scaffold path (relative to scaffoldRoot, with
// forward slashes) to the name it is written under: the literal file
// "gitignore" becomes ".gitignore" — embed.FS's default pattern skips
// dot-prefixed files, so the scaffold keeps that one file's source name
// dot-free and renames it on the way out instead — and any other file's
// ".tmpl" suffix is stripped. A path with neither trait, including every
// directory, passes through unchanged.
func targetName(rel string) string {
	if rel == "gitignore" {
		return ".gitignore"
	}
	return strings.TrimSuffix(rel, ".tmpl")
}

// substitute replaces the scaffold's two placeholder tokens, "@@MODULE@@" and
// "@@NAME@@", with module and name.
//
// Plain string replacement, not text/template, is deliberate: several
// scaffold files are themselves templates for collage's own template engine —
// they contain "{{slot ...}}" — and running them through a second templating
// pass here would try, and fail, to resolve those as this package's actions
// instead of leaving them for the scaffolded project's own template engine.
func substitute(content []byte, module, name string) []byte {
	s := string(content)
	s = strings.ReplaceAll(s, "@@MODULE@@", module)
	s = strings.ReplaceAll(s, "@@NAME@@", name)
	return []byte(s)
}

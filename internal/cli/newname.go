package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Elagoht/collage/internal/ascii"
)

// checkModulePath reports why path cannot be the module path of a new project,
// or nil. It is the essence of golang.org/x/mod/module.CheckImportPath, which is
// what "go mod init" applies, reimplemented because collage uses the standard
// library only: slash-separated elements of ASCII letters, digits and "-._~",
// none empty, none starting or ending with a dot, none a name Windows reserves.
// Without it the name is spliced into go.mod, import paths and Go source as
// typed, and "collage new ." writes "module .".
func checkModulePath(path string) error {
	if path == "" {
		return fmt.Errorf("the module path is empty")
	}
	if path[0] == '-' {
		return fmt.Errorf("module path %q starts with a dash", path)
	}
	for _, element := range strings.Split(path, "/") {
		switch {
		case element == "":
			return fmt.Errorf("module path %q has an empty element (a leading, trailing or doubled slash)", path)
		case element[0] == '.':
			return fmt.Errorf("module path %q has an element starting with a dot", path)
		case element[len(element)-1] == '.':
			return fmt.Errorf("module path %q has an element ending with a dot", path)
		}
		for _, r := range element {
			ok := r < unicode.MaxASCII && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r))
			if !ok {
				return fmt.Errorf("module path %q has %q in it; use letters, digits and - . _ ~", path, r)
			}
		}
		if windowsReserved(element) {
			return fmt.Errorf("module path %q has an element, %q, that Windows reserves as a file name", path, element)
		}
	}
	return nil
}

// windowsReserved reports whether an element's first dot-separated part is a
// device name Windows will not create a file or directory called.
func windowsReserved(element string) bool {
	base, _, _ := strings.Cut(element, ".")
	switch ascii.LowerString(base) {
	case "con", "prn", "aux", "nul":
		return true
	}
	lower := ascii.LowerString(base)
	return len(lower) == 4 && (strings.HasPrefix(lower, "com") || strings.HasPrefix(lower, "lpt")) && lower[3] >= '1' && lower[3] <= '9'
}

// checkDisplayName reports why name cannot be written into the scaffold as the
// project's name. It lands in Go string literals (quoted by substitute), HTML
// (escaped there) and a Go raw string holding a template, so what it must not
// hold is what those cannot carry: a backtick ends the raw string, braces open
// a template action, a path separator is not a name, and anything unprintable is
// not one either.
func checkDisplayName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("the project name is empty")
	}
	if strings.Contains(name, "@@") {
		return fmt.Errorf("project name %q holds @@, which the scaffold uses as a placeholder", name)
	}
	for _, r := range name {
		if !unicode.IsPrint(r) || strings.ContainsRune("`{}/\\", r) {
			return fmt.Errorf("project name %q has %q in it", name, r)
		}
	}
	return nil
}

// isPathTarget reports whether the positional argument of "collage new" names a
// place rather than a project: "." and "..", an absolute path, or anything with a
// separator in it.
func isPathTarget(arg string) bool {
	return arg == "." || arg == ".." || filepath.IsAbs(arg) || strings.ContainsAny(arg, `/\`)
}

// newTarget resolves "collage new"'s positional argument, --dir and --module
// into the directory to scaffold, the project's display name and its module
// path, or the reason none can be had.
//
// A path as the argument — "." above all, the way to start a project in the
// directory you made for it — is where to scaffold, and the project is named
// for that directory's base name, since the path text is not a name. With --dir
// the argument is only a name, and must be one.
func newTarget(arg, dir, module string) (target, name, modulePath string, err error) {
	target, name = dir, arg
	switch {
	case dir == "" && isPathTarget(arg):
		abs, absErr := filepath.Abs(arg)
		if absErr != nil {
			return "", "", "", absErr
		}
		target, name = arg, filepath.Base(abs)
		if filepath.Dir(abs) == abs {
			return "", "", "", fmt.Errorf("%q is a filesystem root and has no name to give the project; name a directory", arg)
		}
	case dir == "":
		target = filepath.Join(".", arg)
	case isPathTarget(arg):
		return "", "", "", fmt.Errorf("with --dir, the project name %q cannot be a path; give a name, or only the path", arg)
	}
	if err := checkDisplayName(name); err != nil {
		return "", "", "", err
	}

	modulePath = module
	if modulePath == "" {
		modulePath = name
		if err := checkModulePath(modulePath); err != nil {
			return "", "", "", fmt.Errorf("%w; pass --module for the module path to use", err)
		}
		return target, name, modulePath, nil
	}
	if err := checkModulePath(modulePath); err != nil {
		return "", "", "", fmt.Errorf("--module: %w", err)
	}
	return target, name, modulePath, nil
}

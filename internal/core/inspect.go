package core

import (
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"sort"

	"github.com/Elagoht/collage/internal/template"
	"github.com/Elagoht/collage/internal/types"
)

// InspectionVersion is the version of the Inspection format. It changes when a
// field is removed or its meaning changes; a field may be added without it.
const InspectionVersion = 1

// Inspection is what an application is made of, as a tool outside it — an
// editor's completion, a linter — needs to know it: every page, fragment,
// document and action by name, the slots each template fills, the template
// functions, the plugins and the files the mounts serve. It is plain data,
// encodable as JSON; `go run . collage-inspect` prints it.
type Inspection struct {
	Version           int                 `json:"version"`
	TemplateRoot      string              `json:"templateRoot"`
	TemplateExtension string              `json:"templateExtension"`
	DefaultLocale     string              `json:"defaultLocale"`
	Locales           []string            `json:"locales"`
	Pages             []InspectedPage     `json:"pages"`
	Fragments         []InspectedFragment `json:"fragments"`
	Documents         []InspectedDocument `json:"documents"`
	Actions           []InspectedAction   `json:"actions"`
	TemplateFuncs     []string            `json:"templateFuncs"`
	Plugins           []InspectedPlugin   `json:"plugins"`
	Mounts            []InspectedMount    `json:"mounts"`
}

// InspectedPage is one registered page.
type InspectedPage struct {
	Name string `json:"name"`
	// Paths are its patterns by locale, as registered.
	Paths map[string]string `json:"paths"`
	// Params are the placeholders its patterns name, in order of appearance.
	Params        []string                `json:"params,omitempty"`
	Strategy      string                  `json:"strategy"`
	Layout        string                  `json:"layout,omitempty"`
	Content       string                  `json:"content,omitempty"`
	FragmentPaths []InspectedFragmentPath `json:"fragmentPaths,omitempty"`
}

// InspectedFragmentPath is one fragment a page opened at a URL of its own.
type InspectedFragmentPath struct {
	Fragment string   `json:"fragment"`
	Locale   string   `json:"locale"`
	Pattern  string   `json:"pattern"`
	Params   []string `json:"params,omitempty"`
}

// InspectedFragment is one fragment some registered page renders.
type InspectedFragment struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	// Slots are the slots it declares or has fragments bound into.
	Slots   []string `json:"slots,omitempty"`
	Handler bool     `json:"handler,omitempty"`
	Static  bool     `json:"static,omitempty"`
	Shared  bool     `json:"shared,omitempty"`
}

// InspectedDocument is one registered document.
type InspectedDocument struct {
	Name        string            `json:"name"`
	Paths       map[string]string `json:"paths"`
	Params      []string          `json:"params,omitempty"`
	ContentType string            `json:"contentType"`
}

// InspectedAction is one registered action a page or the application declared.
type InspectedAction struct {
	Name    string            `json:"name"`
	Paths   map[string]string `json:"paths"`
	Methods []string          `json:"methods"`
}

// InspectedPlugin is one registered plugin.
type InspectedPlugin struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InspectedMount is one mounted file system and the URLs of its files.
type InspectedMount struct {
	Prefix string `json:"prefix"`
	// Files are the URL paths it serves — "/static/app.css" — up to
	// maxInspectedFiles, sorted; Truncated says there were more.
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated,omitempty"`
}

// maxInspectedFiles bounds the files listed per mount: a tool completing asset
// paths needs the site's stylesheets and scripts, not a media library.
const maxInspectedFiles = 5000

var placeholder = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)(\.\.\.)?\}`)

// patternParams returns the placeholders the patterns name, each once, in order.
func patternParams(patterns ...string) []string {
	var out []string
	for _, p := range patterns {
		for _, m := range placeholder.FindAllStringSubmatch(p, -1) {
			if !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}
	return out
}

func sortedValues(m map[string]string) []string {
	keys := slices.Sorted(maps.Keys(m))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

func strategyName(s types.RenderStrategy) string {
	switch s {
	case types.StrategyStatic:
		return "static"
	case types.StrategyIncremental:
		return "incremental"
	case types.StrategyDynamic:
		return "dynamic"
	default:
		return "auto"
	}
}

// Inspect describes the application as registered. Call it after registration;
// it starts nothing and serves nothing. Plugins' pages and documents are included
// once the application has started, since plugins register them in Init.
func (a *App) Inspect() Inspection {
	a.mu.RLock()
	defer a.mu.RUnlock()

	defaultLocale, locales := a.Locales()
	out := Inspection{
		Version:           InspectionVersion,
		TemplateRoot:      a.cfg.Template.Root,
		TemplateExtension: a.cfg.Template.Extension,
		DefaultLocale:     defaultLocale,
		Locales:           locales,
		Pages:             []InspectedPage{},
		Fragments:         []InspectedFragment{},
		Documents:         []InspectedDocument{},
		Actions:           []InspectedAction{},
		Plugins:           []InspectedPlugin{},
		Mounts:            []InspectedMount{},
	}

	seen := make(map[*types.Fragment]bool)
	addFragment := func(f *types.Fragment) error {
		slots := slices.Sorted(maps.Keys(f.Slots))
		out.Fragments = append(out.Fragments, InspectedFragment{
			Name: f.Name, Template: f.TemplatePath, Slots: slots,
			Handler: f.DataHandler != nil, Static: f.Static, Shared: f.Shared,
		})
		return nil
	}
	for _, name := range a.order {
		p := a.pages[name]
		ip := InspectedPage{
			Name:     p.Name,
			Paths:    maps.Clone(p.Paths),
			Params:   patternParams(sortedValues(p.Paths)...),
			Strategy: strategyName(p.Strategy),
		}
		if p.LayoutFragment != nil {
			ip.Layout = p.LayoutFragment.Name
		}
		if p.ContentFragment != nil {
			ip.Content = p.ContentFragment.Name
		}
		for _, locale := range slices.Sorted(maps.Keys(p.FragmentPaths)) {
			for _, pattern := range slices.Sorted(maps.Keys(p.FragmentPaths[locale])) {
				if f := p.FragmentPaths[locale][pattern]; f != nil {
					ip.FragmentPaths = append(ip.FragmentPaths, InspectedFragmentPath{Fragment: f.Name, Locale: locale, Pattern: pattern, Params: patternParams(pattern)})
				}
			}
		}
		out.Pages = append(out.Pages, ip)
		for _, root := range append([]*types.Fragment{p.LayoutFragment, p.ContentFragment}, p.PathFragments()...) {
			_ = walkFragments(root, seen, addFragment)
		}
	}
	sort.SliceStable(out.Fragments, func(i, j int) bool { return out.Fragments[i].Name < out.Fragments[j].Name })

	for _, name := range slices.Sorted(maps.Keys(a.documents)) {
		d := a.documents[name]
		out.Documents = append(out.Documents, InspectedDocument{
			Name: d.Name, Paths: maps.Clone(d.Paths), Params: patternParams(sortedValues(d.Paths)...), ContentType: d.ContentType,
		})
	}
	for _, act := range a.actionOrder {
		if _, fragmentPath := a.fragmentPaths[act]; fragmentPath {
			continue // listed under its page
		}
		out.Actions = append(out.Actions, InspectedAction{Name: act.Name, Paths: maps.Clone(act.Paths), Methods: slices.Clone(act.Methods)})
	}

	funcs := template.DefaultFuncs()
	names := slices.Collect(maps.Keys(funcs))
	names = append(names, slices.Collect(maps.Keys(a.pluginFuncs))...)
	names = append(names, slices.Collect(maps.Keys(a.cfg.Template.Funcs))...)
	slices.Sort(names)
	out.TemplateFuncs = slices.Compact(names)

	for _, p := range a.plugins.Plugins() {
		out.Plugins = append(out.Plugins, InspectedPlugin{Name: p.Name(), Version: p.Version()})
	}

	for _, m := range a.mounts {
		im := InspectedMount{Prefix: m.Prefix(), Files: []string{}}
		_ = fs.WalkDir(m.FS(), ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if len(im.Files) >= maxInspectedFiles {
				im.Truncated = true
				return fs.SkipAll
			}
			im.Files = append(im.Files, m.Prefix()+path)
			return nil
		})
		slices.Sort(im.Files)
		out.Mounts = append(out.Mounts, im)
	}
	return out
}

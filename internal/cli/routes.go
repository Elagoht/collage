package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"
)

const routesUsage = `Usage: collage routes

Lists every URL the current directory's project answers through collage, a
row per method and path, sorted by path: its pages (with how they are cached
and the guards they meet), the fragment URLs they open, the actions on them
and on URLs of their own, the documents and the mounts.

It reads collage-inspect, as collage inspect does, so the project is built
and its newApp runs. What newApp does not register through collage
is not listed: a handler mounted with app.Handle, and a plugin's own
endpoints, such as /healthz or a websocket.
`

// routesInspection is the part of collage-inspect's output routes reads.
type routesInspection struct {
	Version       int      `json:"version"`
	DefaultLocale string   `json:"defaultLocale"`
	Locales       []string `json:"locales"`
	Pages         []struct {
		Name          string            `json:"name"`
		Paths         map[string]string `json:"paths"`
		Strategy      string            `json:"strategy"`
		Guards        []string          `json:"guards"`
		FragmentPaths []struct {
			Fragment string `json:"fragment"`
			Locale   string `json:"locale"`
			Pattern  string `json:"pattern"`
		} `json:"fragmentPaths"`
	} `json:"pages"`
	Documents []struct {
		Name        string            `json:"name"`
		Paths       map[string]string `json:"paths"`
		ContentType string            `json:"contentType"`
	} `json:"documents"`
	Actions []struct {
		Name    string            `json:"name"`
		Paths   map[string]string `json:"paths"`
		Methods []string          `json:"methods"`
		Guarded bool              `json:"guarded"`
	} `json:"actions"`
	Mounts []struct {
		Prefix    string   `json:"prefix"`
		Files     []string `json:"files"`
		Truncated bool     `json:"truncated"`
	} `json:"mounts"`
}

// routesInspectionVersion is the collage-inspect format routes reads.
const routesInspectionVersion = 1

// routeRow is one line of the table.
type routeRow struct {
	method, path, locale, kind, name, note string
}

// runRoutes implements the "routes" command.
func (c *CLI) runRoutes(ctx context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
		fmt.Fprint(c.stdout(), routesUsage)
		return 0
	}
	if len(args) != 0 {
		fmt.Fprintln(c.stderr(), "collage: routes takes no arguments")
		fmt.Fprintln(c.stderr())
		fmt.Fprint(c.stderr(), routesUsage)
		return 2
	}
	var out, errOut bytes.Buffer
	if err := c.runDevProgram(ctx, &out, &errOut, "collage-inspect"); err != nil {
		c.stderr().Write(errOut.Bytes())
		fmt.Fprintf(c.stderr(), "collage: routes: %v\n", err)
		return 1
	}
	var in routesInspection
	if err := json.Unmarshal(out.Bytes(), &in); err != nil {
		fmt.Fprintf(c.stderr(), "collage: routes: collage-inspect printed no description: %v\n", err)
		return 1
	}
	if in.Version != routesInspectionVersion {
		fmt.Fprintf(c.stderr(), "collage: routes: the project describes itself in format %d; this CLI reads %d: install the collage CLI that matches the project's collage\n", in.Version, routesInspectionVersion)
		return 1
	}
	writeRoutes(c.stdout(), in)
	return 0
}

// writeRoutes prints in's routes as a table, with a locale column only when
// the site has more than one locale.
func writeRoutes(w io.Writer, in routesInspection) {
	var rows []routeRow
	pageAt := map[string]string{}     // "locale path" → page name
	guardsOf := map[string][]string{} // page name → guards
	for _, p := range in.Pages {
		guardsOf[p.Name] = p.Guards
		note := p.Strategy
		if len(p.Guards) > 0 {
			note += ", guard " + strings.Join(p.Guards, ", ")
		}
		for _, locale := range slices.Sorted(maps.Keys(p.Paths)) {
			pageAt[locale+" "+p.Paths[locale]] = p.Name
			rows = append(rows, routeRow{"GET", p.Paths[locale], locale, "page", p.Name, note})
		}
		for _, fp := range p.FragmentPaths {
			note := "of page " + p.Name
			if len(p.Guards) > 0 {
				note += ", guard " + strings.Join(p.Guards, ", ")
			}
			rows = append(rows, routeRow{"GET", fp.Pattern, fp.Locale, "fragment", fp.Fragment, note})
		}
	}
	for _, a := range in.Actions {
		for _, locale := range slices.Sorted(maps.Keys(a.Paths)) {
			path := a.Paths[locale]
			note := "own URL, no guard"
			if a.Guarded {
				note = "own URL, own guard"
			}
			if page, ok := pageAt[locale+" "+path]; ok {
				note = "on page " + page
				if g := guardsOf[page]; len(g) > 0 {
					note += ", guard " + strings.Join(g, ", ")
				}
				if a.Guarded {
					note += ", own guard"
				}
			}
			rows = append(rows, routeRow{strings.Join(a.Methods, ","), path, locale, "action", a.Name, note})
		}
	}
	for _, d := range in.Documents {
		for _, locale := range slices.Sorted(maps.Keys(d.Paths)) {
			rows = append(rows, routeRow{"GET", d.Paths[locale], locale, "document", d.Name, d.ContentType})
		}
	}
	for _, m := range in.Mounts {
		files := fmt.Sprintf("%d files", len(m.Files))
		if len(m.Files) == 1 {
			files = "1 file"
		}
		if m.Truncated {
			files = "more than " + files
		}
		rows = append(rows, routeRow{"GET", strings.TrimSuffix(m.Prefix, "/") + "/*", "", "mount", "", files})
	}

	kindOrder := map[string]int{"page": 0, "action": 1, "fragment": 2, "document": 3, "mount": 4}
	slices.SortStableFunc(rows, func(a, b routeRow) int {
		if c := strings.Compare(a.path, b.path); c != 0 {
			return c
		}
		if c := kindOrder[a.kind] - kindOrder[b.kind]; c != 0 {
			return c
		}
		return strings.Compare(a.locale, b.locale)
	})

	locales := len(in.Locales) > 1
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	header := "METHOD\tPATH\tKIND\tNAME\tNOTES"
	if locales {
		header = "METHOD\tPATH\tLOCALE\tKIND\tNAME\tNOTES"
	}
	fmt.Fprintln(tw, header)
	for _, r := range rows {
		if locales {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.method, r.path, r.locale, r.kind, r.name, r.note)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.method, r.path, r.kind, r.name, r.note)
		}
	}
	tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Not listed: handlers mounted with app.Handle, and plugins' own endpoints.")
}

package httpx

import (
	"html"
	"strings"

	"github.com/Elagoht/collage/internal/render"
	"github.com/Elagoht/collage/internal/types"
)

// devProblem is one thing that went wrong in a render a development page should
// show rather than hide.
type devProblem struct {
	fragment string
	detail   string
	fallback bool
	// finding, when set, is what the problem is: a plugin's finding about the
	// page rather than a fragment that failed.
	finding *types.Finding
}

// devFindings turns the findings plugins reported about a render into problems
// for the overlay. Outside development there is nothing to show.
func (h *Handler) devFindings(findings []types.Finding) []devProblem {
	if !h.devMode {
		return nil
	}
	problems := make([]devProblem, 0, len(findings))
	for i := range findings {
		problems = append(problems, devProblem{finding: &findings[i], detail: findings[i].Message})
	}
	return problems
}

// devProblems lists the fragments of result that failed, for a page that rendered
// anyway. Outside development there is nothing to show and nothing is collected.
func (h *Handler) devProblems(result *render.Result) []devProblem {
	if !h.devMode || result == nil || result.Metadata == nil {
		return nil
	}
	var problems []devProblem
	for _, fragment := range result.Metadata.Fragments {
		if !fragment.Failed {
			continue
		}
		problems = append(problems, devProblem{
			fragment: fragment.Name,
			detail:   errorDetail(fragment.Err),
			fallback: fragment.UsedFallback,
		})
	}
	return problems
}

// overlayHeading says what the overlay is about: a failure when any fragment
// failed, the checks' findings when nothing did.
func overlayHeading(problems []devProblem) string {
	for _, p := range problems {
		if p.finding == nil {
			return "this page rendered, but part of it failed"
		}
	}
	return "checks found something on this page"
}

// withDevOverlay puts a panel naming problems over a development page, fixed to
// the bottom of the viewport so it stays in sight while the page scrolls.
//
// It exists because a page that renders with a broken part looks, in development,
// exactly like a page with nothing there: the failure went into an HTML comment, or
// a fallback covered it, and the one person who could fix it has to open the source
// to find out. So the page says so, over itself, with the fragment, the error
// and — for a template — the file and line html/template put in the message.
//
// Only ever at write time, never into what is cached, and only in development:
// error text is exactly what a production page must never carry.
func withDevOverlay(page []byte, heading string, problems []devProblem) []byte {
	if len(problems) == 0 {
		return page
	}

	var b strings.Builder
	b.WriteString(`<div id="collage-dev-overlay" role="alert" style="position:fixed;inset:auto 1rem 1rem 1rem;z-index:2147483647;` +
		`max-height:60vh;overflow:auto;padding:1rem 1.25rem;border-radius:.5rem;background:#1b1b1f;color:#f4f4f5;` +
		`font:14px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace;box-shadow:0 10px 40px rgba(0,0,0,.45);border-left:4px solid #e4572e">`)
	b.WriteString(`<button type="button" onclick="this.parentElement.remove()" aria-label="Dismiss" ` +
		`style="float:right;background:none;border:0;color:inherit;font-size:1.25rem;cursor:pointer">×</button>`)
	b.WriteString(`<strong style="color:#e4572e">collage</strong> · `)
	b.WriteString(html.EscapeString(heading))
	for _, problem := range problems {
		if f := problem.finding; f != nil {
			color := "#f3b61f"
			if f.Level == types.FindingError {
				color = "#e4572e"
			}
			b.WriteString(`<div style="margin-top:.75rem"><div><span style="color:` + color + `">`)
			b.WriteString(html.EscapeString(f.Level.String()))
			b.WriteString(`</span> <code>`)
			b.WriteString(html.EscapeString(f.Rule))
			b.WriteString(`</code> <span style="color:#a1a1aa">`)
			b.WriteString(html.EscapeString(f.Plugin))
			b.WriteString(`</span></div><pre style="margin:.25rem 0 0;white-space:pre-wrap;color:#d4d4d8">`)
			b.WriteString(html.EscapeString(problem.detail))
			b.WriteString(`</pre></div>`)
			continue
		}
		if problem.fragment == "" {
			// Nothing below the page failed: a hook, the route, the page itself.
			b.WriteString(`<div style="margin-top:.75rem"><div>the page itself`)
		} else {
			b.WriteString(`<div style="margin-top:.75rem"><div>fragment <code style="color:#f3b61f">`)
			b.WriteString(html.EscapeString(problem.fragment))
			b.WriteString(`</code>`)
		}
		if problem.fallback {
			b.WriteString(` — its fallback rendered in its place`)
		}
		b.WriteString(`</div><pre style="margin:.25rem 0 0;white-space:pre-wrap;color:#d4d4d8">`)
		b.WriteString(html.EscapeString(problem.detail))
		b.WriteString(`</pre></div>`)
	}
	b.WriteString(`</div>`)

	return insertBeforeBodyEnd(page, b.String())
}

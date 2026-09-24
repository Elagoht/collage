package httpx

import (
	"html"
	"strings"

	"github.com/Elagoht/collage/internal/render"
)

// devProblem is one thing that went wrong in a render a development page should
// show rather than hide.
type devProblem struct {
	fragment string
	detail   string
	fallback bool
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

// withDevOverlay puts a panel naming problems at the top of a development page.
//
// It exists because a page that renders with a broken part looks, in development,
// exactly like a page with nothing there: the failure went into an HTML comment, or
// a fallback covered it, and the one person who could fix it has to open the source
// to find out. So the page says so, on top of itself, with the fragment, the error
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

package httpx

import (
	"html"
	"strconv"
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

// devOverlayStyle styles the overlay: a dialog over the middle of the page, and a
// button at the bottom right that brings it back once minimized. Scoped to the
// overlay's id, and reset with all:initial, so neither the page's CSS nor this
// one reaches the other. The state attribute on the root is all the script there
// is: open shows the dialog, min shows the button, and closing removes the lot.
const devOverlayStyle = `<style>
#collage-dev-overlay,#collage-dev-overlay *{all:revert;box-sizing:border-box}
#collage-dev-overlay{all:initial;font:14px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace;color:#f4f4f5}
#collage-dev-overlay .cdo-backdrop{position:fixed;inset:0;z-index:2147483647;display:flex;align-items:center;justify-content:center;padding:1rem;background:rgba(9,9,11,.55)}
#collage-dev-overlay .cdo-dialog{width:min(48rem,100%);max-height:80vh;overflow:auto;padding:1rem 1.25rem;border-radius:.5rem;background:#1b1b1f;color:#f4f4f5;font:inherit;box-shadow:0 20px 60px rgba(0,0,0,.5);border-left:4px solid #e4572e}
#collage-dev-overlay .cdo-head{display:flex;align-items:flex-start;gap:.5rem}
#collage-dev-overlay .cdo-title{flex:1;margin:0;font:inherit}
#collage-dev-overlay .cdo-head button{background:none;border:0;padding:0 .25rem;color:#a1a1aa;font-family:inherit;font-size:1.25rem;line-height:1;cursor:pointer}
#collage-dev-overlay .cdo-head button:hover,#collage-dev-overlay .cdo-head button:focus-visible{color:#f4f4f5}
#collage-dev-overlay .cdo-item{margin-top:.75rem}
#collage-dev-overlay code{font:inherit}
#collage-dev-overlay pre{margin:.25rem 0 0;white-space:pre-wrap;color:#d4d4d8;font:inherit}
#collage-dev-overlay .cdo-fab{position:fixed;right:1rem;bottom:1rem;z-index:2147483647;display:flex;align-items:center;gap:.5rem;padding:.5rem .875rem;border:1px solid #3f3f46;border-radius:999px;background:#1b1b1f;color:#f4f4f5;font:13px/1.2 ui-monospace,SFMono-Regular,Menlo,monospace;box-shadow:0 4px 16px rgba(0,0,0,.4);cursor:pointer}
#collage-dev-overlay .cdo-dot{width:.5rem;height:.5rem;border-radius:50%}
#collage-dev-overlay[data-state=open] .cdo-fab,#collage-dev-overlay[data-state=min] .cdo-backdrop{display:none}
</style>`

// withDevOverlay puts a dialog naming problems over the middle of a development
// page, with a button to minimize it to a corner and one to close it for good.
//
// It exists because a page that renders with a broken part looks, in development,
// exactly like a page with nothing there: the failure went into an HTML comment, or
// a fallback covered it, and the one person who could fix it has to open the source
// to find out. So the page says so, over itself, with the fragment, the error
// and — for a template — the file and line html/template put in the message.
//
// It goes in last, after anything a plugin put before </body>, so it is the last
// element of the page and sits above a plugin's own panel rather than under it.
// Minimized, it is a button at the bottom right, out of the way of one at the left.
//
// Only ever at write time, never into what is cached, and only in development:
// error text is exactly what a production page must never carry.
func withDevOverlay(page []byte, heading string, problems []devProblem) []byte {
	if len(problems) == 0 {
		return page
	}

	accent := "#f3b61f"
	failures, findings := 0, 0
	for _, p := range problems {
		switch {
		case p.finding == nil:
			failures++
			accent = "#e4572e"
		default:
			findings++
			if p.finding.Level == types.FindingError {
				accent = "#e4572e"
			}
		}
	}

	var b strings.Builder
	b.WriteString(`<div id="collage-dev-overlay" data-state="open">`)
	b.WriteString(devOverlayStyle)
	b.WriteString(`<div class="cdo-backdrop" onclick="if(event.target===this)this.parentElement.dataset.state='min'">`)
	b.WriteString(`<div class="cdo-dialog" role="alertdialog" aria-modal="true" aria-labelledby="collage-dev-overlay-title">`)
	b.WriteString(`<div class="cdo-head"><p class="cdo-title" id="collage-dev-overlay-title"><strong style="color:#e4572e">collage</strong> · `)
	b.WriteString(html.EscapeString(heading))
	b.WriteString(`</p><button type="button" onclick="this.closest('#collage-dev-overlay').dataset.state='min'" aria-label="Minimize" title="Minimize">–</button>`)
	b.WriteString(`<button type="button" onclick="this.closest('#collage-dev-overlay').remove()" aria-label="Close" title="Close">×</button></div>`)
	for _, problem := range problems {
		if f := problem.finding; f != nil {
			color := "#f3b61f"
			if f.Level == types.FindingError {
				color = "#e4572e"
			}
			b.WriteString(`<div class="cdo-item"><div><span style="color:` + color + `">`)
			b.WriteString(html.EscapeString(f.Level.String()))
			b.WriteString(`</span> <code>`)
			b.WriteString(html.EscapeString(f.Rule))
			b.WriteString(`</code> <span style="color:#a1a1aa">`)
			b.WriteString(html.EscapeString(f.Plugin))
			b.WriteString(`</span></div><pre>`)
			b.WriteString(html.EscapeString(problem.detail))
			b.WriteString(`</pre></div>`)
			continue
		}
		if problem.fragment == "" {
			// Nothing below the page failed: a hook, the route, the page itself.
			b.WriteString(`<div class="cdo-item"><div>the page itself`)
		} else {
			b.WriteString(`<div class="cdo-item"><div>fragment <code style="color:#f3b61f">`)
			b.WriteString(html.EscapeString(problem.fragment))
			b.WriteString(`</code>`)
		}
		if problem.fallback {
			b.WriteString(` — its fallback rendered in its place`)
		}
		b.WriteString(`</div><pre>`)
		b.WriteString(html.EscapeString(problem.detail))
		b.WriteString(`</pre></div>`)
	}
	b.WriteString(`</div></div>`)
	b.WriteString(`<button type="button" class="cdo-fab" onclick="this.parentElement.dataset.state='open'" aria-label="Show collage problems">`)
	b.WriteString(`<span class="cdo-dot" style="background:` + accent + `"></span><strong style="color:#e4572e">collage</strong> `)
	b.WriteString(fabLabel(failures, findings))
	b.WriteString(`</button></div>`)

	return insertBeforeBodyEnd(page, b.String())
}

// fabLabel counts what the minimized overlay stands for: failures first, since
// they are what broke the page, then findings.
func fabLabel(failures, findings int) string {
	var parts []string
	if failures > 0 {
		parts = append(parts, plural(failures, "failure", "failures"))
	}
	if findings > 0 {
		parts = append(parts, plural(findings, "finding", "findings"))
	}
	return strings.Join(parts, " · ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

package httpx

import (
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// slotFailure is the chain a real render produces when a page's template calls a
// slot its fragment does not declare: the fragment's own error, returned by the
// slot function, carried up through two template executions.
func slotFailure(t *testing.T) error {
	t.Helper()
	cause := fmt.Errorf("%w: fragment %q has no slot %q, only %v", types.ErrUnknownSlot, "recipe-content", "aside", []string{"more"})
	inner := template.Must(template.New("pages/recipe.html").Funcs(template.FuncMap{
		"slot": func(string) (template.HTML, error) { return "", cause },
	}).Parse("<main>\n  {{slot \"aside\"}}\n</main>"))
	innerErr := inner.Execute(io.Discard, nil)

	outer := template.Must(template.New("layouts/main.html").Funcs(template.FuncMap{
		"slot": func(string) (template.HTML, error) {
			return "", fmt.Errorf("collage: fragment %q template pages/recipe.html: %w", "recipe-content", innerErr)
		},
	}).Parse("<body>{{slot \"content\"}}</body>"))
	return fmt.Errorf("collage: fragment %q template layouts/main.html: %w", "layout", outer.Execute(io.Discard, nil))
}

// The development page leads with what went wrong and where, ahead of the chain
// that carried it up: the cause is otherwise the tail of one long line.
func TestDevBuiltinPageLeadsWithTheCause(t *testing.T) {
	body := string(builtinPage(failure{status: http.StatusInternalServerError, err: slotFailure(t), fragment: "recipe-content"}, true))

	cause := escaped(`collage: unknown slot: fragment "recipe-content" has no slot "aside", only [more]`)
	where := "pages/recipe.html:2:4"
	chain := strings.Index(body, escaped(`fragment "layout" template layouts/main.html`))
	if chain < 0 {
		t.Fatalf("the full chain is missing:\n%s", body)
	}
	for _, want := range []string{cause, where} {
		if i := strings.Index(body, want); i < 0 || i > chain {
			t.Errorf("%q does not come before the chain:\n%s", want, body)
		}
	}
}

func escaped(s string) string { return template.HTMLEscapeString(s) }

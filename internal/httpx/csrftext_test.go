package httpx

import (
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// User-visible text that merely looks like a forgery marker — a post quoting one,
// a changelog — must not make a page uncacheable. Only the rendered field, a
// value attribute, is something Personalise would replace, and escaped text
// cannot produce a quote of its own.
func TestHandler_TextThatLooksLikeAMarkerDoesNotDisableCaching(t *testing.T) {
	page := testPage("post", "/post", types.StrategyIncremental)
	lookalike := "collage-csrf-" + strings.Repeat("a", 32)
	env := newEnv(t, []*types.Page{page})
	env.engine.set("post", fakeRender{html: "<p>the marker is " + lookalike + " and value=&#34;" + lookalike + "&#34;</p>"})

	for range 3 {
		if rec := env.get("/post"); rec.Code != 200 {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
	if got := env.engine.pageCalls("post"); got != 1 {
		t.Fatalf("renders = %d, want 1: the cached page was dropped as carrying a foreign marker", got)
	}
}

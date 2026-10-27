package httpx

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/csrf"
	"github.com/Elagoht/collage/internal/types"
)

// failingRandom is a nonce source that always fails.
type failingRandom struct{}

func (failingRandom) Read([]byte) (int, error) { return 0, errors.New("no randomness") }

// A page whose form needs a token that cannot be issued is a failure, answered
// 500, never a body carrying the marker: the marker is a string that later
// content could plant, and a form holding it is refused on submission.
func TestCSRF_TokenFailureIsAServerError(t *testing.T) {
	guard, err := csrf.New(csrf.Config{Key: []byte("a key for the tests"), Random: failingRandom{}})
	if err != nil {
		t.Fatal(err)
	}
	page := testPage("form", "/form", types.StrategyDynamic)
	engine := newFakeEngine(fakeRender{html: `<input name="_csrf" value="` + guard.Marker() + `">`})
	env := newEnv(t, []*types.Page{page}, func(d *Deps) {
		d.CSRF = guard
		d.Renderer = engine
	})

	res := env.get("/form")
	if res.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", res.Code)
	}
	if strings.Contains(res.Body.String(), guard.Marker()) {
		t.Errorf("body carries the forgery marker: %q", res.Body.String())
	}
	for _, c := range res.Result().Cookies() {
		if c.Name == "collage_csrf" {
			t.Errorf("a token cookie was sent with the failure: %v", c)
		}
	}
}

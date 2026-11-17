package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/csrf"
	"github.com/Elagoht/collage/internal/types"
)

// A urlencoded form past net/http's own 10 MB form cap is a body too large to
// read, a 413, whatever MaxBodyBytes allows. ParseForm reports it as a plain
// "http: POST too large", not a MaxBytesError, and the forgery check used to
// take that for a bad token and answer 403.
func TestCSRF_AFormPastNetHTTPsCapIsTooLargeNotForbidden(t *testing.T) {
	option, guard := withCSRF(t)
	create := action("create", "/posts", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) { return nil, nil })
	create.MaxBodyBytes = 64 << 20

	env := actionEnv(t, nil, []*types.Action{create}, option)
	token, _, _ := guard.TokenFor(httptest.NewRequest(http.MethodGet, "/", nil))
	req := post("/posts", "title="+strings.Repeat("x", 11<<20)+"&"+csrf.DefaultFieldName+"="+token)
	req.AddCookie(&http.Cookie{Name: csrf.DefaultCookieName, Value: token})

	if res := env.do(req); res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", res.Code)
	}
}

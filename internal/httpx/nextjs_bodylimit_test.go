package httpx

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// Next.js e2e/middleware-fetches-with-body: a body read in middleware is held
// to the limit, as the handler's own read is.
func TestNextjs_MiddlewareReadsAreBounded(t *testing.T) {
	var read int
	create := action("create", "/posts", []string{http.MethodPost},
		func(context.Context, *types.RenderContext) (*types.ActionResult, error) { return nil, nil })
	create.MaxBodyBytes = 16
	mw := func(d *Deps) {
		d.MaxBodyBytes = 16
		d.Middleware = append(d.Middleware, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				read = len(b)
				next.ServeHTTP(w, r)
			})
		})
	}
	env := actionEnv(t, nil, []*types.Action{create}, mw)
	env.do(post("/posts", strings.Repeat("x", 1<<20)))
	if read > 16 {
		t.Errorf("middleware read %d bytes of a body the application bounds at 16", read)
	}
}

// A middleware that parsed the form must not leave the action to answer a body
// its own limit refuses: the body was read past its bound all the same.
func TestNextjs_MiddlewareParseSkipsTheActionLimit(t *testing.T) {
	ran := false
	create := action("create", "/posts", []string{http.MethodPost},
		func(_ context.Context, rc *types.RenderContext) (*types.ActionResult, error) {
			ran = true
			return nil, rc.Request.ParseForm()
		})
	create.MaxBodyBytes = 16
	mw := func(d *Deps) {
		d.Middleware = append(d.Middleware, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				next.ServeHTTP(w, r)
			})
		})
	}
	env := actionEnv(t, nil, []*types.Action{create}, mw)
	res := env.do(post("/posts", "title="+strings.Repeat("x", 4096)))
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d (handler ran: %v), want 413 for a body 256x the action's limit", res.Code, ran)
	}
}


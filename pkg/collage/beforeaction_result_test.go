package collage_test

// A BeforeActionHook's Result is a full answer, made what a handler's result is
// made: refused when ambiguous, and its InvalidateTags invalidated before the
// response, as a handler's are.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// answering is a plugin answering every action itself with result.
type answering struct{ result func() *collage.ActionResult }

func (p *answering) Name() string                             { return "test/answering" }
func (p *answering) Version() string                          { return "0" }
func (p *answering) Init(context.Context, collage.Host) error { return nil }
func (p *answering) Shutdown(context.Context) error           { return nil }
func (p *answering) OnBeforeAction(_ context.Context, ev *collage.BeforeActionEvent) error {
	ev.Result = p.result()
	return nil
}

func answeringSite(t *testing.T, result func() *collage.ActionResult) (*collagetest.Site, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var renders, handled atomic.Int32
	board := collage.NewFragment("board", "board.html").WithData(collage.LoadTagged(
		func(context.Context, *collage.RenderContext) (string, []string, error) {
			return fmt.Sprint(renders.Add(1)), []string{"board"}, nil
		})).Build()
	page := collage.NewPage("board").WithContent(board).WithPath("en", "/board").Static().
		WithAction(http.MethodPost, func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			handled.Add(1)
			return nil, nil
		}).Build()
	site := collagetest.NewSite(t,
		collagetest.WithPlugins(&answering{result: result}),
		collagetest.WithTemplates(map[string]string{"board.html": `<p>render {{.}}</p><form method="post">{{csrfToken}}</form>`}),
		collagetest.WithRegister(page),
		collagetest.WithConfig(func(cfg *collage.Config) {
			cfg.Cache = collage.CacheConfig{Enabled: true, Type: collage.CacheMemory, DefaultTTL: time.Hour}
		}))
	return site, &renders, &handled
}

func TestBeforeAction_ResultInvalidatesItsTags(t *testing.T) {
	site, renders, handled := answeringSite(t, func() *collage.ActionResult {
		return &collage.ActionResult{Location: "/board", InvalidateTags: []string{"board"}}
	})
	page := site.Get("/board").WantStatus(http.StatusOK)
	site.Get("/board").WantStatus(http.StatusOK)
	if renders.Load() != 1 {
		t.Fatalf("renders before the action = %d, want 1 (cached)", renders.Load())
	}
	res := site.Submit(page, "", url.Values{})
	if res.Status != http.StatusSeeOther {
		t.Fatalf("submit status = %d, want 303", res.Status)
	}
	if handled.Load() != 0 {
		t.Errorf("the handler ran after the hook answered")
	}
	after := site.Get("/board").WantStatus(http.StatusOK)
	if !strings.Contains(after.Body, "render 2") {
		t.Errorf("after the hook's InvalidateTags the page is %q, want a fresh render", after.Body)
	}
}

func TestBeforeAction_AmbiguousResultIsRefused(t *testing.T) {
	site, _, _ := answeringSite(t, func() *collage.ActionResult {
		return &collage.ActionResult{Location: "/board", Body: []byte("x"), InvalidateTags: []string{"board"}}
	})
	page := site.Get("/board").WantStatus(http.StatusOK)
	res := site.Submit(page, "", url.Values{})
	if res.Status != http.StatusInternalServerError {
		t.Errorf("ambiguous hook result status = %d, want 500", res.Status)
	}
	if after := site.Get("/board"); !strings.Contains(after.Body, "render 1") {
		t.Errorf("a refused result invalidated: %q", after.Body)
	}
}

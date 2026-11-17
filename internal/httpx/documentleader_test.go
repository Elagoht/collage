package httpx

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/types"
)

// A document's handler runs for every reader waiting on its key, so it must not
// end with the reader that started it. A feed handler that tolerates upstream
// errors would otherwise return a partial body, which every waiter would be
// handed and the cache would keep.
func TestDocument_LeaderLeavingDoesNotCancelTheHandlerOrLoseTheEntry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var handlerErr error
	doc := &types.Document{
		Name: "feed", ContentType: "application/xml",
		Paths:    map[string]string{"en": "/feed.xml"},
		Strategy: types.StrategyStatic,
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			cancel() // the reader leaves while the handler is working
			// The cancellation reaches a derived context through a goroutine, so
			// give it a moment to arrive rather than reading Err at once.
			select {
			case <-ctx.Done():
			case <-time.After(100 * time.Millisecond):
			}
			handlerErr = ctx.Err()
			return []byte("<feed/>"), []string{"feed"}, nil
		},
	}
	h, mem := documentEnv(t, doc, false)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/feed.xml", nil).WithContext(ctx))

	if handlerErr != nil {
		t.Errorf("handler saw ctx.Err() = %v, want nil: the run is shared and outlives its leader", handlerErr)
	}
	if got := mem.Stats().Entries; got != 1 {
		t.Errorf("cache entries = %d, want 1: the write ran under the leader's cancelled context", got)
	}
}

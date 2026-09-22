package render

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Elagoht/collage/internal/observability"
	"github.com/Elagoht/collage/internal/types"
)

func documentContext() *types.RenderContext {
	req := httptest.NewRequest("GET", "/sitemap.xml", nil)
	return types.NewRenderContext(context.Background(), req, nil, "en", nil)
}

func TestExecuteDocument_ReturnsBodyContentTypeAndSortedTags(t *testing.T) {
	engine := New(nil, Options{})
	doc := &types.Document{
		Name:           "sitemap",
		ContentType:    "application/xml",
		DependencyTags: []string{"site", "site"},
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("<urlset/>"), []string{"blog:posts", "blog:posts", ""}, nil
		},
	}

	result, err := engine.ExecuteDocument(context.Background(), doc, documentContext())
	if err != nil {
		t.Fatalf("ExecuteDocument() = %v", err)
	}
	if string(result.Body) != "<urlset/>" {
		t.Fatalf("Body = %q", result.Body)
	}
	if result.ContentType != "application/xml" {
		t.Fatalf("ContentType = %q, want application/xml", result.ContentType)
	}
	if want := []string{"blog:posts", "site"}; !slices.Equal(result.Tags, want) {
		t.Fatalf("Tags = %v, want %v — deduplicated, sorted, empties dropped", result.Tags, want)
	}
}

func TestExecuteDocument_NotFoundIsClassifiedNotSwallowed(t *testing.T) {
	engine := New(nil, Options{})
	doc := &types.Document{
		Name:        "post-json",
		ContentType: "application/json",
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, fmt.Errorf("looking up post: %w", types.ErrNotFound)
		},
	}

	result, err := engine.ExecuteDocument(context.Background(), doc, documentContext())
	if err == nil {
		t.Fatal("ExecuteDocument() = nil error, want the handler's error — NotFound classifies, it does not succeed")
	}
	if result == nil {
		t.Fatal("Result = nil, want a non-nil result on every path")
	}
	if !result.NotFound {
		t.Fatal("NotFound = false, want true")
	}
	if result.Body != nil {
		t.Fatalf("Body = %q, want nil on failure", result.Body)
	}
}

func TestExecuteDocument_OrdinaryErrorIsNotNotFound(t *testing.T) {
	engine := New(nil, Options{})
	doc := &types.Document{
		Name:        "feed",
		ContentType: "application/rss+xml",
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, errors.New("database unavailable")
		},
	}

	result, err := engine.ExecuteDocument(context.Background(), doc, documentContext())
	if err == nil {
		t.Fatal("ExecuteDocument() = nil, want an error")
	}
	if result.NotFound {
		t.Fatal("NotFound = true, want false for an ordinary error")
	}
}

func TestExecuteDocument_PanicIsContained(t *testing.T) {
	engine := New(nil, Options{})
	doc := &types.Document{
		Name:        "boom",
		ContentType: "text/plain",
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			panic("handler exploded")
		},
	}

	result, err := engine.ExecuteDocument(context.Background(), doc, documentContext())
	if err == nil {
		t.Fatal("ExecuteDocument() = nil, want an error")
	}
	var panicErr *PanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("error = %v, want a *PanicError", err)
	}
	if result == nil {
		t.Fatal("Result = nil, want a non-nil result even after a panic")
	}
}

func TestExecuteDocument_TimeoutBoundsAContextRespectingHandler(t *testing.T) {
	engine := New(nil, Options{DefaultTimeout: 20 * time.Millisecond})
	doc := &types.Document{
		Name:        "slow",
		ContentType: "text/plain",
		Handler: func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		},
	}

	start := time.Now()
	if _, err := engine.ExecuteDocument(context.Background(), doc, documentContext()); err == nil {
		t.Fatal("ExecuteDocument() = nil, want a deadline error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("took %v, want the handler bounded by its 20ms timeout", elapsed)
	}
}

func TestExecuteDocument_ReportsRenderDurationOnBothPaths(t *testing.T) {
	tests := []struct {
		name    string
		handler types.DocumentHandlerFunc
	}{
		{"success", func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return []byte("ok"), nil, nil
		}},
		{"failure", func(ctx context.Context, rc *types.RenderContext) ([]byte, []string, error) {
			return nil, nil, errors.New("boom")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := observability.NewRecordingMetrics()
			engine := New(nil, Options{Metrics: metrics})
			doc := &types.Document{Name: "doc", ContentType: "text/plain", Handler: test.handler}

			engine.ExecuteDocument(context.Background(), doc, documentContext())

			if got := len(metrics.Snapshot().RenderDurations); got != 1 {
				t.Fatalf("RenderDurations = %d, want 1 — an observability layer blind to failures is blind to what matters", got)
			}
		})
	}
}

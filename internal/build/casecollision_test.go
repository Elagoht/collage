package build

import (
	"context"
	"errors"
	"testing"

	"github.com/Elagoht/collage/internal/types"
)

// TestBuild_OutputCollisionIgnoresCase is StaticParams listing "Go" and "go": two
// paths to a router, one file on the default macOS and Windows file systems,
// where the second write silently replaced the first while the report listed
// both. Folded the same way on every platform, so a build behaves the same
// wherever it runs.
func TestBuild_OutputCollisionIgnoresCase(t *testing.T) {
	cases := map[string]struct {
		pages     []*types.Page
		documents []*types.Document
	}{
		"two pages": {pages: []*types.Page{
			newTestPage("upper", types.StrategyStatic, map[string]string{"en": "/tags/Go"}),
			newTestPage("lower", types.StrategyStatic, map[string]string{"en": "/tags/go"}),
		}},
		"two documents": {documents: []*types.Document{
			newTestDocument("upper", types.StrategyStatic, map[string]string{"en": "/Feed.xml"}),
			newTestDocument("lower", types.StrategyStatic, map[string]string{"en": "/feed.xml"}),
		}},
		"a file where another needs the directory": {
			pages:     []*types.Page{newTestPage("sub", types.StrategyStatic, map[string]string{"en": "/Feed.xml/sub"})},
			documents: []*types.Document{newTestDocument("feed", types.StrategyStatic, map[string]string{"en": "/feed.xml"})},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b, err := New(&fakeRenderer{pages: c.pages, documents: c.documents, defaultLocale: "en"}, Options{OutDir: resolvedTempDir(t)})
			if err != nil {
				t.Fatal(err)
			}
			report, err := b.Build(context.Background())
			if !errors.Is(err, ErrOutputPathCollision) {
				t.Fatalf("Build error = %v, want ErrOutputPathCollision", err)
			}
			if len(report.Written) != 0 {
				t.Errorf("Written = %v, want nothing written", report.Written)
			}
		})
	}
}

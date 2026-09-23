package newsroom

import (
	"errors"
	"testing"
)

func TestNewStore_EmbeddedCorpusIsConsistent(t *testing.T) {
	// The corpus is embedded, so this is a build artefact check: it fails if
	// content.json gains an article naming a category or author that is not
	// declared, or two articles sharing a slug.
	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if got := store.List(Filter{PerPage: MaxPerPage}).Total; got < 10 {
		t.Errorf("corpus holds %d articles, want at least 10 for the listings to be worth paginating", got)
	}
	if len(store.Categories()) == 0 || len(store.Authors()) == 0 {
		t.Error("corpus declares no categories or no authors")
	}
}

func TestLoadStore_RejectsUnknownCategory(t *testing.T) {
	raw := []byte(`{
		"categories": [{"slug":"tech","name":"Tech"}],
		"authors": [{"slug":"ada","name":"Ada"}],
		"articles": [{"slug":"a","title":"A","category":"missing","author":"ada","publishedAt":"2026-01-01T00:00:00Z"}]
	}`)
	if _, err := LoadStore(raw); err == nil {
		t.Fatal("LoadStore() error = nil, want a referential integrity error (the category does not exist)")
	}
}

func TestLoadStore_RejectsDuplicateSlug(t *testing.T) {
	raw := []byte(`{
		"categories": [{"slug":"tech","name":"Tech"}],
		"authors": [{"slug":"ada","name":"Ada"}],
		"articles": [
			{"slug":"a","title":"One","category":"tech","author":"ada","publishedAt":"2026-01-01T00:00:00Z"},
			{"slug":"a","title":"Two","category":"tech","author":"ada","publishedAt":"2026-01-02T00:00:00Z"}
		]
	}`)
	if _, err := LoadStore(raw); err == nil {
		t.Fatal("LoadStore() error = nil, want a duplicate slug error (one slug is one URL)")
	}
}

func TestStore_ListIsNewestFirst(t *testing.T) {
	store := mustStore(t)
	page := store.List(Filter{PerPage: MaxPerPage})

	for i := 1; i < len(page.Items); i++ {
		if page.Items[i-1].PublishedAt.Before(page.Items[i].PublishedAt) {
			t.Fatalf("item %d (%s) is older than item %d (%s); listings must be newest first",
				i-1, page.Items[i-1].PublishedAt, i, page.Items[i].PublishedAt)
		}
	}
}

func TestStore_ListPaginates(t *testing.T) {
	store := mustStore(t)
	all := store.List(Filter{PerPage: MaxPerPage})

	first := store.List(Filter{PerPage: 3, Page: 1})
	second := store.List(Filter{PerPage: 3, Page: 2})

	if len(first.Items) != 3 || len(second.Items) != 3 {
		t.Fatalf("page sizes = %d and %d, want 3 and 3", len(first.Items), len(second.Items))
	}
	if first.Items[0].Slug == second.Items[0].Slug {
		t.Error("page 2 repeats page 1; the offset is not being applied")
	}
	if first.Total != all.Total {
		t.Errorf("Total = %d on a paged listing, want the unpaged total %d", first.Total, all.Total)
	}
	if want := (all.Total + 2) / 3; first.TotalPages != want {
		t.Errorf("TotalPages = %d, want %d", first.TotalPages, want)
	}
}

func TestStore_ListPastTheEndIsEmptyNotAnError(t *testing.T) {
	// A reader who edits the page number in the URL gets an empty listing, not a
	// 500. The site renders "nothing here"; the counts still describe the corpus.
	store := mustStore(t)
	page := store.List(Filter{Page: 900})

	if len(page.Items) != 0 {
		t.Errorf("Items = %d, want 0", len(page.Items))
	}
	if page.Total == 0 {
		t.Error("Total = 0, want the true corpus total even on an out-of-range page")
	}
	if page.HasNext() {
		t.Error("HasNext() = true on a page past the end")
	}
}

func TestStore_ListClampsPerPage(t *testing.T) {
	store := mustStore(t)
	if got := store.List(Filter{PerPage: 100000}).PerPage; got != MaxPerPage {
		t.Errorf("PerPage = %d, want it clamped to %d", got, MaxPerPage)
	}
	if got := store.List(Filter{PerPage: -5}).PerPage; got != DefaultPerPage {
		t.Errorf("PerPage = %d, want the default %d", got, DefaultPerPage)
	}
}

func TestStore_ListFiltersCombine(t *testing.T) {
	store := mustStore(t)
	cats := store.Categories()
	target := cats[0].Slug

	page := store.List(Filter{Category: target, PerPage: MaxPerPage})
	if page.Total == 0 {
		t.Fatalf("category %q matched nothing", target)
	}
	for _, art := range page.Items {
		if art.Category != target {
			t.Errorf("article %q has category %q, want %q", art.Slug, art.Category, target)
		}
	}
}

func TestStore_SearchMatchesTitleNotBody(t *testing.T) {
	// Matches deliberately ignores the body. This pins that decision down: a word
	// that appears only in a body paragraph must not pull the article into results.
	store := mustStore(t)
	art, err := store.Article("the-index-that-ate-the-database")
	if err != nil {
		t.Fatalf("Article() error = %v", err)
	}

	if !art.Matches("index") {
		t.Error("Matches(\"index\") = false, want true (the word is in the title)")
	}
	if art.Matches("eleven thousand inserts") {
		t.Error("Matches() = true for a body-only phrase; the body is not searched")
	}
}

func TestStore_ArticleNotFoundIsErrNotFound(t *testing.T) {
	store := mustStore(t)
	_, err := store.Article("no-such-article")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Article() error = %v, want ErrNotFound", err)
	}
}

func TestStore_PopularIsStableAndRanked(t *testing.T) {
	store := mustStore(t)
	first := store.Popular(5)
	second := store.Popular(5)

	if len(first) != 5 {
		t.Fatalf("Popular(5) returned %d articles, want 5", len(first))
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].Views < first[i].Views {
			t.Errorf("Popular is not ranked: %d views before %d views", first[i-1].Views, first[i].Views)
		}
	}
	for i := range first {
		if first[i].Slug != second[i].Slug {
			t.Fatal("Popular returned a different order on a second call; the ordering must be stable or every cached page differs for no reason")
		}
	}
}

func TestArticle_PathMatchesTheRegisteredRouteShape(t *testing.T) {
	store := mustStore(t)
	art, err := store.Article("seawalls-buy-time-not-safety")
	if err != nil {
		t.Fatalf("Article() error = %v", err)
	}
	if want := "/2026/09/seawalls-buy-time-not-safety"; art.Path() != want {
		t.Errorf("Path() = %q, want %q", art.Path(), want)
	}
}

func mustStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}

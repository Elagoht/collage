package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// ErrStorageUnavailable reports that a post exists but its body could not be
// loaded. It is deliberately NOT wrapped around collage.ErrNotFound: this is the
// "something broke" half of the distinction the framework draws, and it is what
// makes a request for BrokenSlug render the blog's own 500 page rather than its
// 404 page.
var ErrStorageUnavailable = errors.New("blog: post storage unavailable")

// BrokenSlug names the one post in the seeded store whose body always fails to
// load, so the example exercises the page-specific 500 page end to end. It is not
// returned by List: the index is served from its own healthy source, and only the
// body of this post is unreachable.
const BrokenSlug = "storage-outage"

// Post is one blog post.
type Post struct {
	// Slug is the post's URL segment, the value the "/blog/{slug}" route
	// captures.
	Slug string
	// Title is the post's headline.
	Title string
	// Body is the post's rendered text.
	Body string
	// Published is when the post went live.
	Published time.Time
}

// PostStore is the example's in-memory post store. It stands in for whatever a
// real application would read from — a database, a CMS, a directory of Markdown
// files — and is safe for concurrent use, because the framework serves every
// request on its own goroutine.
//
// It also counts how many times each post was loaded. That counter is what the
// end-to-end test uses to tell a cache hit from a re-render: both answer 200 with
// the same body, so a status code cannot distinguish them.
type PostStore struct {
	mu sync.RWMutex
	// posts holds the index, in the order the home page lists it.
	posts []Post
	// bodies maps a slug to its full post, including the ones missing from the
	// index.
	bodies map[string]Post
	// loads counts the Post calls made for each slug, including the ones that
	// failed.
	loads map[string]int
}

// NewPostStore returns a store seeded with the example's posts, plus the one
// broken post named by BrokenSlug.
func NewPostStore() *PostStore {
	seeded := []Post{
		{
			Slug:      "hello-collage",
			Title:     "Hello, collage",
			Body:      "A page is a layout, a content fragment, and the slots between them.",
			Published: time.Date(2026, time.January, 12, 9, 0, 0, 0, time.UTC),
		},
		{
			Slug:      "fragments-all-the-way-down",
			Title:     "Fragments all the way down",
			Body:      "Every fragment owns its own data handler, its own timeout, and its own failure policy.",
			Published: time.Date(2026, time.February, 3, 9, 0, 0, 0, time.UTC),
		},
	}

	store := &PostStore{
		posts:  seeded,
		bodies: make(map[string]Post, len(seeded)+1),
		loads:  make(map[string]int),
	}
	for _, post := range seeded {
		store.bodies[post.Slug] = post
	}
	store.bodies[BrokenSlug] = Post{
		Slug:      BrokenSlug,
		Title:     "The post whose body never loads",
		Published: time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC),
	}
	return store
}

// List returns the post index, newest last, as a copy the caller may keep.
func (s *PostStore) List() []Post {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Post(nil), s.posts...)
}

// Post returns the post stored under slug.
//
// It reports two different failures, and the difference is the whole point:
//
//   - an unknown slug returns an error wrapping collage.ErrNotFound, which makes
//     the framework serve the page's NotFoundPage with a 404;
//   - BrokenSlug returns an error wrapping ErrStorageUnavailable, an ordinary
//     failure, which makes the framework serve the page's ErrorPage with a 500.
//
// ctx is honoured the way every data handler's context must be: a cancelled
// request stops the load rather than running it to completion for nobody.
func (s *PostStore) Post(ctx context.Context, slug string) (Post, error) {
	if err := ctx.Err(); err != nil {
		return Post{}, fmt.Errorf("blog: load post %q: %w", slug, err)
	}

	s.mu.Lock()
	s.loads[slug]++
	post, ok := s.bodies[slug]
	s.mu.Unlock()

	if !ok {
		return Post{}, fmt.Errorf("blog: no post with slug %q: %w", slug, collage.ErrNotFound)
	}
	if slug == BrokenSlug {
		return Post{}, fmt.Errorf("blog: load post %q: %w", slug, ErrStorageUnavailable)
	}
	return post, nil
}

// Loads returns how many times Post has been called for slug. A cached response
// does not reach the store, so this counter only advances on a real render.
func (s *PostStore) Loads(slug string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loads[slug]
}

package collage

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestFragmentBuilder_MinimalApp builds the fragments of an application modelled
// on the README's minimal one: a layout fragment declaring a required content
// slot, and a home-content fragment with a DataHandler returning a data map and a
// tag slice.
func TestFragmentBuilder_MinimalApp(t *testing.T) {
	layout := NewFragment("layout", "layouts/default.html").
		WithSlot("content", true, false).
		Build()

	homeContent := NewFragment("home-content", "pages/home.html").
		WithDataHandler(func(ctx context.Context, rc *RenderContext) (any, []string, error) {
			data := map[string]any{
				"Title": "Welcome Home",
			}
			tags := []string{"homepage"}
			return data, tags, nil
		}).
		Build()

	if layout.Name != "layout" {
		t.Errorf("layout.Name = %q, want %q", layout.Name, "layout")
	}
	if layout.TemplatePath != "layouts/default.html" {
		t.Errorf("layout.TemplatePath = %q, want %q", layout.TemplatePath, "layouts/default.html")
	}
	slot, ok := layout.Slot("content")
	if !ok {
		t.Fatal("layout has no \"content\" slot")
	}
	if !slot.Required {
		t.Error("slot.Required = false, want true")
	}
	if slot.AllowMultiple {
		t.Error("slot.AllowMultiple = true, want false")
	}
	if len(slot.Fill) != 0 {
		t.Errorf("slot.Fill = %v, want empty — the builder never binds content into the layout, registration does", slot.Fill)
	}

	if homeContent.Name != "home-content" {
		t.Errorf("homeContent.Name = %q, want %q", homeContent.Name, "home-content")
	}
	if homeContent.TemplatePath != "pages/home.html" {
		t.Errorf("homeContent.TemplatePath = %q, want %q", homeContent.TemplatePath, "pages/home.html")
	}
	if homeContent.DataHandler == nil {
		t.Fatal("homeContent.DataHandler = nil, want set")
	}
	data, tags, err := homeContent.DataHandler(context.Background(), &RenderContext{})
	if err != nil {
		t.Fatalf("DataHandler() error = %v, want nil", err)
	}
	dataMap, ok := data.(map[string]any)
	if !ok || dataMap["Title"] != "Welcome Home" {
		t.Errorf("DataHandler() data = %v, want map[Title:Welcome Home]", data)
	}
	if len(tags) != 1 || tags[0] != "homepage" {
		t.Errorf("DataHandler() tags = %v, want [homepage]", tags)
	}
}

// TestFragmentBuilder_BlogPostContent builds the content fragment of a typical
// blog post page: a Required() fragment whose DataHandler reads a path parameter,
// fetches a post, and either returns the post tagged "post:<slug>" or an error that
// would trigger the page's custom 500. fetchPost is a local stand-in for an
// application's data access.
func TestFragmentBuilder_BlogPostContent(t *testing.T) {
	fetchPost := func(slug string) (*fetchedPost, error) {
		if slug == "" {
			return nil, errors.New("post not found")
		}
		return &fetchedPost{Slug: slug}, nil
	}

	blogPostContent := NewFragment("blog-post", "pages/blog-post.html").
		WithDataHandler(func(ctx context.Context, rc *RenderContext) (any, []string, error) {
			slug := rc.PathParams["slug"]
			post, err := fetchPost(slug)
			if err != nil {
				return nil, nil, err // Will trigger custom 500 page
			}
			return post, []string{fmt.Sprintf("post:%s", slug)}, nil
		}).
		Required().
		Build()

	if blogPostContent.Name != "blog-post" {
		t.Errorf("Name = %q, want %q", blogPostContent.Name, "blog-post")
	}
	if blogPostContent.TemplatePath != "pages/blog-post.html" {
		t.Errorf("TemplatePath = %q, want %q", blogPostContent.TemplatePath, "pages/blog-post.html")
	}
	if !blogPostContent.Required {
		t.Error("Required = false, want true")
	}

	rc := &RenderContext{PathParams: map[string]string{"slug": "hello-world"}}
	data, tags, err := blogPostContent.DataHandler(context.Background(), rc)
	if err != nil {
		t.Fatalf("DataHandler() error = %v, want nil for a known slug", err)
	}
	if data == nil {
		t.Error("DataHandler() data = nil, want the fetched post")
	}
	if len(tags) != 1 || tags[0] != "post:hello-world" {
		t.Errorf("DataHandler() tags = %v, want [post:hello-world]", tags)
	}

	rcMissing := &RenderContext{PathParams: map[string]string{}}
	if _, _, err := blogPostContent.DataHandler(context.Background(), rcMissing); err == nil {
		t.Error("DataHandler() error = nil, want an error for a missing slug (triggers the 500 page)")
	}
}

func TestFragmentBuilder_Build_NeverNil(t *testing.T) {
	f := NewFragment("", "").Build()
	if f == nil {
		t.Fatal("Build() = nil, want non-nil Fragment even for a builder with no calls")
	}
}

// TestFragmentBuilder_BuildErr table-drives every documented builder error case.
func TestFragmentBuilder_BuildErr(t *testing.T) {
	tests := []struct {
		name    string
		build   func() *FragmentBuilder
		wantErr error
	}{
		{
			name: "duplicate slot",
			build: func() *FragmentBuilder {
				return NewFragment("parent", "parent.html").
					WithSlot("content", false, false).
					WithSlot("content", true, true)
			},
			wantErr: ErrDuplicateSlot,
		},
		{
			name: "unknown slot binding",
			build: func() *FragmentBuilder {
				child := NewFragment("child", "child.html").Build()
				return NewFragment("parent", "parent.html").
					WithSlotFragment("missing", child)
			},
			wantErr: ErrUnknownSlot,
		},
		{
			name: "negative timeout",
			build: func() *FragmentBuilder {
				return NewFragment("f", "f.html").WithTimeout(-1 * time.Second)
			},
			wantErr: ErrInvalidTimeout,
		},
		{
			name: "nil slot fragment",
			build: func() *FragmentBuilder {
				return NewFragment("parent", "parent.html").
					WithSlot("content", false, false).
					WithSlotFragment("content", nil)
			},
			wantErr: ErrNilFragment,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.build()

			f := b.Build()
			if f == nil {
				t.Fatal("Build() = nil, want non-nil Fragment")
			}

			err := b.BuildErr()
			if err == nil {
				t.Fatalf("BuildErr() = nil, want error wrapping %v", tt.wantErr)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("BuildErr() = %v, want error wrapping %v", err, tt.wantErr)
			}
		})
	}
}

func TestFragmentBuilder_BuildErr_NilWhenNoErrors(t *testing.T) {
	b := NewFragment("home", "home.html").WithTimeout(time.Second)
	b.Build()
	if err := b.BuildErr(); err != nil {
		t.Fatalf("BuildErr() = %v, want nil", err)
	}
}

// fetchedPost is what the blog-post fixtures' stand-in store returns. It is a
// concrete type rather than an "any" or a map[string]any so that the stand-in states
// what it hands back: only the DataHandler signature itself has a genuine reason to
// be untyped, since html/template renders arbitrary data, and a test helper borrowing
// that looseness would be borrowing an exception it has no claim to.
type fetchedPost struct {
	// Slug is the post's URL slug, which is all these fixtures read.
	Slug string
}

type clockView struct{ Hour int }

// A typed handler adapted by DataHandler hands its value through unchanged, tags
// and all.
func TestDataHandler_PassesTheTypedValueThrough(t *testing.T) {
	handler := DataHandler(func(context.Context, *RenderContext) (clockView, []string, error) {
		return clockView{Hour: 9}, []string{"clock"}, nil
	})

	data, tags, err := handler(context.Background(), &RenderContext{})
	if err != nil {
		t.Fatalf("handler() error = %v, want nil", err)
	}
	if view, ok := data.(clockView); !ok || view.Hour != 9 {
		t.Errorf("data = %#v, want clockView{Hour: 9}", data)
	}
	if len(tags) != 1 || tags[0] != "clock" {
		t.Errorf("tags = %v, want [clock]", tags)
	}
}

// On an error the data is dropped, so a nil pointer does not become a typed nil
// that a template reads as present.
func TestDataHandler_DropsDataOnError(t *testing.T) {
	failure := errors.New("upstream down")
	handler := DataHandler(func(context.Context, *RenderContext) (*clockView, []string, error) {
		return nil, []string{"clock"}, failure
	})

	data, tags, err := handler(context.Background(), &RenderContext{})
	if !errors.Is(err, failure) {
		t.Fatalf("handler() error = %v, want %v", err, failure)
	}
	if data != nil {
		t.Errorf("data = %#v, want an untyped nil", data)
	}
	if len(tags) != 1 {
		t.Errorf("tags = %v, want the handler's tags kept", tags)
	}
}

func TestDataHandler_NilIsNil(t *testing.T) {
	var fn func(context.Context, *RenderContext) (clockView, []string, error)
	if DataHandler(fn) != nil {
		t.Error("DataHandler(nil) != nil, want a nil handler")
	}
}

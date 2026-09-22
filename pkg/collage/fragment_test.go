package collage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestFragmentBuilder_MinimalApp mirrors the framework's minimal single-fragment
// usage: a data handler attached in one fluent chain and built directly, as in the
// package doc comment's example (collage.NewFragment(...).WithDataHandler(h).Build()).
func TestFragmentBuilder_MinimalApp(t *testing.T) {
	handler := func(ctx context.Context, rc *RenderContext) (any, []string, error) {
		return "hello", nil, nil
	}

	f := NewFragment("home", "pages/home.html").WithDataHandler(handler).Build()

	if f == nil {
		t.Fatal("Build() = nil, want non-nil Fragment")
	}
	if f.Name != "home" {
		t.Errorf("Name = %q, want %q", f.Name, "home")
	}
	if f.TemplatePath != "pages/home.html" {
		t.Errorf("TemplatePath = %q, want %q", f.TemplatePath, "pages/home.html")
	}
	if f.DataHandler == nil {
		t.Error("DataHandler = nil, want set")
	}
}

// TestFragmentBuilder_BlogLayout mirrors a blog-style layout: a layout fragment
// declaring a required "content" slot, with a required post fragment bound into that
// slot, carrying its own fallback and DataHandler timeout.
func TestFragmentBuilder_BlogLayout(t *testing.T) {
	fallback := NewFragment("post-fallback", "pages/post_fallback.html").Build()

	post := NewFragment("post", "pages/post.html").
		WithDataHandler(func(ctx context.Context, rc *RenderContext) (any, []string, error) {
			return nil, []string{"post:slug"}, nil
		}).
		Required().
		WithFallback(fallback).
		WithTimeout(2 * time.Second).
		Build()

	layoutBuilder := NewFragment("layout", "layout.html").
		WithSlot("content", true, false).
		WithSlotFragment("content", post)
	layout := layoutBuilder.Build()

	if err := layoutBuilder.BuildErr(); err != nil {
		t.Fatalf("BuildErr() = %v, want nil", err)
	}

	if layout.Name != "layout" {
		t.Errorf("layout.Name = %q, want %q", layout.Name, "layout")
	}
	slot, ok := layout.Slot("content")
	if !ok {
		t.Fatal("layout has no \"content\" slot")
	}
	if !slot.Required {
		t.Error("slot.Required = false, want true")
	}
	if len(slot.Fill) != 1 || slot.Fill[0] != post {
		t.Fatalf("slot.Fill = %v, want [post]", slot.Fill)
	}

	if !post.Required {
		t.Error("post.Required = false, want true")
	}
	if post.Fallback != fallback {
		t.Errorf("post.Fallback = %v, want %v", post.Fallback, fallback)
	}
	if post.Timeout != 2*time.Second {
		t.Errorf("post.Timeout = %v, want 2s", post.Timeout)
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

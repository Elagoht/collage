package types

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewRenderContext_DoesNotAliasCallerParams(t *testing.T) {
	params := map[string]string{"id": "1"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	page := &Page{Name: "home"}

	rc := NewRenderContext(context.Background(), req, page, "en", params)

	// Mutate the caller's map after construction; the context must be unaffected.
	params["id"] = "mutated"
	params["extra"] = "added-after-construction"

	if got := rc.Param("id"); got != "1" {
		t.Fatalf("Param(id) = %q, want %q — RenderContext aliased the caller's map", got, "1")
	}
	if _, ok := rc.PathParams["extra"]; ok {
		t.Fatal("PathParams gained a key added to the caller's map after construction")
	}
	if len(rc.PathParams) != 1 {
		t.Fatalf("PathParams = %v, want exactly 1 entry", rc.PathParams)
	}
}

func TestNewRenderContext_DefaultsNilContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rc := NewRenderContext(nil, req, &Page{Name: "home"}, "en", nil) // nolint:staticcheck // SA1012: deliberate nil ctx, exercising the documented default-to-Background behaviour
	if rc.Context() == nil {
		t.Fatal("Context() = nil, want context.Background()")
	}
}

func TestNewRenderContext_InitialisesSharedData(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	rc.Set("k", "v")
	got, ok := rc.Get("k")
	if !ok || got != "v" {
		t.Fatalf("Get(k) = (%v, %v), want (v, true)", got, ok)
	}
}

func TestRenderContext_WithContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	page := &Page{Name: "home"}
	rc := NewRenderContext(context.Background(), req, page, "en", map[string]string{"id": "1"})
	rc.Set("k", "v")

	type ctxKey string
	newCtx := context.WithValue(context.Background(), ctxKey("k"), "v")
	rc2 := rc.WithContext(newCtx)

	if rc2.Context() != newCtx {
		t.Fatal("WithContext() did not install the new context")
	}
	if rc2.Request != rc.Request || rc2.Page != rc.Page || rc2.Locale != rc.Locale {
		t.Fatal("WithContext() did not preserve Request, Page, and Locale")
	}
	if got := rc2.Param("id"); got != "1" {
		t.Fatalf("WithContext() copy lost PathParams: Param(id) = %q", got)
	}
	if got, ok := rc2.Get("k"); !ok || got != "v" {
		t.Fatalf("WithContext() copy lost SharedData: Get(k) = (%v, %v)", got, ok)
	}

	// Maps are shared with the original by design: a write through the copy must be
	// visible on the original.
	rc2.Set("k2", "v2")
	if _, ok := rc.Get("k2"); !ok {
		t.Fatal("WithContext() copy's SharedData is not shared with the original, contradicting its documented shallow-copy behaviour")
	}
}

func TestRenderContext_Param(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", map[string]string{"id": "1"})
	if got := rc.Param("id"); got != "1" {
		t.Fatalf("Param(id) = %q, want %q", got, "1")
	}
	if got := rc.Param("missing"); got != "" {
		t.Fatalf("Param(missing) = %q, want empty string", got)
	}
}

func TestRenderContext_GetSet(t *testing.T) {
	rc := NewRenderContext(context.Background(), nil, nil, "en", nil)
	if _, ok := rc.Get("missing"); ok {
		t.Fatal("Get(missing) reported ok = true, want false")
	}
	rc.Set("key", 42)
	got, ok := rc.Get("key")
	if !ok || got != 42 {
		t.Fatalf("Get(key) = (%v, %v), want (42, true)", got, ok)
	}
}

func TestRenderContext_SetIsNilMapSafe(t *testing.T) {
	rc := &RenderContext{}
	rc.Set("key", "value")
	got, ok := rc.Get("key")
	if !ok || got != "value" {
		t.Fatalf("Set() on a nil SharedData map failed: Get(key) = (%v, %v)", got, ok)
	}
}

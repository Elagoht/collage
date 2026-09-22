package types

import (
	"errors"
	"testing"
	"time"
)

func TestRedirect_EffectiveStatus(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		permanent  bool
		want       int
	}{
		{"301 explicit", 301, false, 301},
		{"302 explicit", 302, true, 302},
		{"307 explicit", 307, false, 307},
		{"308 explicit", 308, true, 308},
		{"zero and permanent", 0, true, 301},
		{"zero and not permanent", 0, false, 302},
		{"non-standard code and permanent", 404, true, 301},
		{"non-standard code and not permanent", 404, false, 302},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Redirect{StatusCode: tt.statusCode, Permanent: tt.permanent}
			if got := r.EffectiveStatus(); got != tt.want {
				t.Errorf("EffectiveStatus() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRedirect_Validate(t *testing.T) {
	tests := []struct {
		name    string
		r       *Redirect
		wantErr error
	}{
		{"valid with explicit status", &Redirect{From: "/a", To: "/b", StatusCode: 301}, nil},
		{"valid with zero status", &Redirect{From: "/a", To: "/b"}, nil},
		{"empty from", &Redirect{From: "", To: "/b"}, ErrInvalidPath},
		{"from missing leading slash", &Redirect{From: "a", To: "/b"}, ErrInvalidPath},
		{"empty to", &Redirect{From: "/a", To: ""}, ErrInvalidPath},
		{"to missing leading slash", &Redirect{From: "/a", To: "b"}, ErrInvalidPath},
		{"invalid status code", &Redirect{From: "/a", To: "/b", StatusCode: 404}, ErrInvalidRedirectStatus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.r.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestPage_Root(t *testing.T) {
	content := &Fragment{Name: "content", TemplatePath: "content.html"}
	layout := &Fragment{Name: "layout", TemplatePath: "layout.html"}

	t.Run("with layout", func(t *testing.T) {
		p := &Page{ContentFragment: content, LayoutFragment: layout}
		if got := p.Root(); got != layout {
			t.Fatalf("Root() = %v, want layout", got)
		}
	})

	t.Run("without layout", func(t *testing.T) {
		p := &Page{ContentFragment: content}
		if got := p.Root(); got != content {
			t.Fatalf("Root() = %v, want content", got)
		}
	})
}

func TestPage_LocalesAndPathFor(t *testing.T) {
	p := &Page{Paths: map[string]string{"tr": "/tr/home", "en": "/en/home"}}

	gotLocales := p.Locales()
	wantLocales := []string{"en", "tr"}
	if len(gotLocales) != len(wantLocales) {
		t.Fatalf("Locales() = %v, want %v", gotLocales, wantLocales)
	}
	for i := range wantLocales {
		if gotLocales[i] != wantLocales[i] {
			t.Fatalf("Locales() = %v, want %v", gotLocales, wantLocales)
		}
	}

	if path, ok := p.PathFor("en"); !ok || path != "/en/home" {
		t.Fatalf("PathFor(en) = (%q, %v), want (/en/home, true)", path, ok)
	}
	if _, ok := p.PathFor("fr"); ok {
		t.Fatal("PathFor(fr) = true, want false for an absent locale")
	}
}

func TestPage_ContentSlotName(t *testing.T) {
	p := &Page{}
	if got := p.ContentSlotName(); got != DefaultContentSlot {
		t.Fatalf("ContentSlotName() = %q, want %q", got, DefaultContentSlot)
	}
}

func TestPage_Validate(t *testing.T) {
	validContent := func() *Fragment {
		return &Fragment{Name: "content", TemplatePath: "content.html"}
	}

	t.Run("valid page", func(t *testing.T) {
		p := &Page{Name: "home", ContentFragment: validContent(), Paths: map[string]string{"en": "/en"}}
		if err := p.Validate(); err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
	})

	t.Run("empty name", func(t *testing.T) {
		p := &Page{ContentFragment: validContent()}
		if err := p.Validate(); !errors.Is(err, ErrEmptyName) {
			t.Fatalf("Validate() error = %v, want ErrEmptyName", err)
		}
	})

	t.Run("missing content fragment", func(t *testing.T) {
		p := &Page{Name: "home"}
		if err := p.Validate(); !errors.Is(err, ErrMissingContent) {
			t.Fatalf("Validate() error = %v, want ErrMissingContent", err)
		}
	})

	t.Run("path missing leading slash", func(t *testing.T) {
		p := &Page{Name: "home", ContentFragment: validContent(), Paths: map[string]string{"en": "en"}}
		if err := p.Validate(); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("Validate() error = %v, want ErrInvalidPath", err)
		}
	})

	t.Run("negative cache ttl", func(t *testing.T) {
		p := &Page{Name: "home", ContentFragment: validContent(), CacheTTL: -time.Second}
		if err := p.Validate(); !errors.Is(err, ErrInvalidTTL) {
			t.Fatalf("Validate() error = %v, want ErrInvalidTTL", err)
		}
	})

	t.Run("incremental strategy without ttl", func(t *testing.T) {
		p := &Page{Name: "home", ContentFragment: validContent(), Strategy: StrategyIncremental}
		if err := p.Validate(); !errors.Is(err, ErrMissingTTL) {
			t.Fatalf("Validate() error = %v, want ErrMissingTTL", err)
		}
	})

	t.Run("incremental strategy with ttl", func(t *testing.T) {
		p := &Page{Name: "home", ContentFragment: validContent(), Strategy: StrategyIncremental, CacheTTL: time.Minute}
		if err := p.Validate(); err != nil {
			t.Fatalf("Validate() error = %v, want nil", err)
		}
	})

	t.Run("invalid redirect propagates", func(t *testing.T) {
		p := &Page{Name: "home", ContentFragment: validContent(), Redirects: []*Redirect{{From: "bad", To: "/b"}}}
		if err := p.Validate(); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("Validate() error = %v, want ErrInvalidPath", err)
		}
	})

	t.Run("invalid root fragment propagates", func(t *testing.T) {
		p := &Page{Name: "home", ContentFragment: &Fragment{TemplatePath: "x.html"}} // empty name
		if err := p.Validate(); !errors.Is(err, ErrEmptyName) {
			t.Fatalf("Validate() error = %v, want ErrEmptyName", err)
		}
	})

	t.Run("self not found page", func(t *testing.T) {
		p := &Page{Name: "home", ContentFragment: validContent()}
		p.NotFoundPage = p
		if err := p.Validate(); !errors.Is(err, ErrSelfErrorPage) {
			t.Fatalf("Validate() error = %v, want ErrSelfErrorPage", err)
		}
	})

	t.Run("self error page", func(t *testing.T) {
		p := &Page{Name: "home", ContentFragment: validContent()}
		p.ErrorPage = p
		if err := p.Validate(); !errors.Is(err, ErrSelfErrorPage) {
			t.Fatalf("Validate() error = %v, want ErrSelfErrorPage", err)
		}
	})
}

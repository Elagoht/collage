package collage_test

// A static build refuses a StaticParams value holding a byte that is a path
// separator on some file system, or one the router never hands a page: on
// Windows a backslash would turn "x\..\..\about" into another page's file.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

func TestBuild_StaticParamsRefusesSeparatorBytes(t *testing.T) {
	for _, c := range []struct{ pattern, slug string }{
		{"/blog/{slug}", `x\..\..\about`},
		{"/blog/{slug}", `a\b`},
		{"/blog/{slug...}", `a/b\..\..\..\about`},
		{"/blog/post-{slug}", `x\y`},
		{"/blog/{slug}", "a\x00b"},
	} {
		t.Run(c.slug, func(t *testing.T) {
			slug := c.slug
			post := collage.NewPage("post").
				WithContent(collage.NewFragment("post", "post.html").Build()).
				WithPath("en", c.pattern).Static().
				WithStaticParams(func(context.Context, string) ([]map[string]string, error) {
					return []map[string]string{{"slug": slug}}, nil
				}).Build()
			site := collagetest.NewSite(t,
				collagetest.WithTemplates(map[string]string{"post.html": `POST`}),
				collagetest.WithRegister(post))

			out := filepath.Join(t.TempDir(), "out")
			_, err := collage.Build(context.Background(), site.App, collage.BuildOptions{OutDir: out})
			if !errors.Is(err, collage.ErrRouteParams) {
				t.Fatalf("Build() = %v, want ErrRouteParams", err)
			}
			if _, statErr := os.Stat(filepath.Join(out, "blog")); !os.IsNotExist(statErr) {
				t.Errorf("something was written under blog/: %v", statErr)
			}
		})
	}
}

// An ordinary value still builds, a backslash-free one with dots and dashes
// included.
func TestBuild_StaticParamsOrdinaryValueBuilds(t *testing.T) {
	post := collage.NewPage("post").
		WithContent(collage.NewFragment("post", "post.html").Build()).
		WithPath("en", "/blog/{slug}").Static().
		WithStaticParams(func(context.Context, string) ([]map[string]string, error) {
			return []map[string]string{{"slug": "v1.2-notes"}}, nil
		}).Build()
	site := collagetest.NewSite(t,
		collagetest.WithTemplates(map[string]string{"post.html": `POST`}),
		collagetest.WithRegister(post))
	out := filepath.Join(t.TempDir(), "out")
	if _, err := collage.Build(context.Background(), site.App, collage.BuildOptions{OutDir: out}); err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "blog", "v1.2-notes", "index.html")); err != nil {
		t.Error(err)
	}
}

# Canonical usage examples (from the architecture specification)

These are the examples the specification defines the public API against. They are the
authority for `pkg/collage`'s surface: a change that stops one of these compiling or
behaving as written is an API break, and the builder tests must mirror them.

## Minimal application

```go
package main

import (
	"context"
	"log"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

func main() {
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{
			Host: "localhost",
			Port: 3000,
		},
		Template: collage.TemplateConfig{
			Root:      "./templates",
			Extension: ".html",
			DevMode:   true,
		},
		Cache: collage.CacheConfig{
			Enabled:    true,
			Type:       "memory",
			DefaultTTL: 5 * time.Minute,
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	layout := collage.NewFragment("layout", "layouts/default.html").
		WithSlot("content", true, false).
		Build()

	homeContent := collage.NewFragment("home-content", "pages/home.html").
		WithDataHandler(func(ctx context.Context, rc *collage.RenderContext) (interface{}, []string, error) {
			data := map[string]interface{}{
				"Title": "Welcome Home",
			}
			tags := []string{"homepage"}
			return data, tags, nil
		}).
		Build()

	homePage := collage.NewPage("home").
		WithLayout(layout).
		WithContent(homeContent).
		WithPath("en", "/").
		Incremental(5 * time.Minute).
		Build()

	app.RegisterPage(homePage)

	log.Println("Starting server on :3000")
	if err := app.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
```

## Advanced example: redirects and custom error pages

```go
blog404Content := collage.NewFragment("blog-404", "errors/blog-404.html").
	Build()

blog404Page := collage.NewPage("blog-404").
	WithLayout(layout).
	WithContent(blog404Content).
	Build()

blog500Content := collage.NewFragment("blog-500", "errors/blog-500.html").
	Build()

blog500Page := collage.NewPage("blog-500").
	WithLayout(layout).
	WithContent(blog500Content).
	Build()

blogPostContent := collage.NewFragment("blog-post", "pages/blog-post.html").
	WithDataHandler(func(ctx context.Context, rc *collage.RenderContext) (interface{}, []string, error) {
		slug := rc.PathParams["slug"]
		post, err := fetchPost(slug)
		if err != nil {
			return nil, nil, err // Will trigger custom 500 page
		}
		return post, []string{fmt.Sprintf("post:%s", slug)}, nil
	}).
	Required().
	Build()

blogPostPage := collage.NewPage("blog-post").
	WithLayout(layout).
	WithContent(blogPostContent).
	WithPath("en", "/blog/{slug}").
	WithPath("tr", "/blog/{slug}").
	WithRedirect("/old-blog/{slug}", "/blog/{slug}", 301).  // Permanent
	WithRedirect("/temp-blog/{slug}", "/blog/{slug}", 302). // Temporary
	WithNotFoundPage(blog404Page).
	WithErrorPage(blog500Page).
	Incremental(10 * time.Minute).
	WithDependency("blog:posts").
	Build()

app.RegisterPage(blogPostPage)
app.RegisterPage(blog404Page)
app.RegisterPage(blog500Page)

globalNotFound := collage.NewPage("global-404").
	WithContent(collage.NewFragment("404", "errors/404.html").Build()).
	Build()

globalError := collage.NewPage("global-500").
	WithContent(collage.NewFragment("500", "errors/500.html").Build()).
	Build()

app.RegisterNotFoundPage(globalNotFound)
app.RegisterErrorPage(globalError)
```

## Note on `interface{}` in these examples

The specification wrote `interface{}` in the `DataHandlerFunc` signature and in the
data map. Under this project's no-`any` constraint the framework declares that return
as `any` with an `// any:` justification (`any` and `interface{}` are the same type, so
the examples above still compile verbatim). User code may write either spelling; the
framework's own source uses `any` only at the three justified declaration sites.

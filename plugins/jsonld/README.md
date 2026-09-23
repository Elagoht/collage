# elagoht/jsonld

A collage plugin that emits schema.org structured data into the document head.

```go
app, err := collage.New(&collage.Config{
	Plugins: []collage.Plugin{jsonld.New()},
})
```

It has no `Configure` phase, so `RegisterPlugin` accepts it as well.

A page contributes its own data from a data handler:

```go
func articleData(ctx context.Context, rc *collage.RenderContext) (any, []string, error) {
	article, err := client.Article(ctx, rc.Param("slug"))
	if err != nil {
		return nil, nil, err
	}
	jsonld.Emit(rc, jsonld.Article{
		Headline:      article.Title,
		Description:   article.Dek,
		DatePublished: article.PublishedAt,
		AuthorName:    article.Author,
		Keywords:      article.Tags,
	})
	return view{Article: article}, nil, nil
}
```

`Emit` appends, so a breadcrumb fragment and a content fragment can each contribute
without knowing about the other.

## Why it reads the render's data and not the HTML

The data handler already fetched the article. Parsing the headline back out of the
markup in order to describe that markup is slower, and wrong the first time a
template changes. The framework hands the render's shared data to `AfterRender`, so
the plugin reads what the page was built *from*.

## Types, not maps

JSON-LD is open-ended, so the obvious shape is `map[string]any` — and then nothing
catches a misspelled property, the compiler has nothing to say about a missing one,
and every application invents its own spelling of the same vocabulary.

`Article`, `WebSite` and `BreadcrumbList` cover what a content site actually emits.
`Raw` is the escape hatch for anything else, and it makes the escape explicit rather
than making it the default — it also refuses invalid JSON, because an unparseable
block inside a `<script>` tag can make a consumer discard every other node on the
page with it.

## Configuration

```json
{
  "elagoht/jsonld": {
    "siteName": "The Wire",
    "siteURL": "https://thewire.example",
    "searchURL": "https://thewire.example/search?q={query}",
    "disabled": false
  }
}
```

With `siteName` set, a `WebSite` node is added to every page that does not emit one
of its own. `searchURL` turns it into a `SearchAction`.

## What it will not do

- **Emit an empty block.** A page with nothing to say gets no `<script>` tag. A
  consumer that fails to parse an empty one may distrust the whole page.
- **Invent a place to write.** A page whose layout produces no `<head>` gets
  nothing.
- **Fail a page.** A node that cannot be marshalled is logged and skipped. Structured
  data is an enhancement, and a 500 for a malformed one would be the plugin deciding
  otherwise on the site's behalf.

## The one security property

The HTML parser ends a `<script>` element at the first `</script` in its text,
wherever it appears — including inside a JSON string. A headline containing one
would close the block early and spill the rest of the JSON into the page as markup.

`encoding/json` escapes `<`, `>` and `&` by default, and that is what prevents it.
The escaping is held twice over — each node's `MarshalJSON` escapes when it marshals
its wire struct, and `json.Marshal` re-escapes a `Marshaler`'s output when it
compacts it — so disabling one layer changes nothing and disabling both opens the
hole. `TestPlugin_ClosingScriptTagInContentCannotEscape` is what notices.

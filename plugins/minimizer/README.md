# elagoht/minimizer

A collage plugin that strips whitespace and comments from what an application
serves: rendered pages, documents, and mounted assets.

```go
import "example.com/minimizer"

app, err := collage.New(&collage.Config{
	Plugins: []collage.Plugin{minimizer.New()},
})
```

It must be supplied through `Config.Plugins`, not `RegisterPlugin`: it wraps mounted
filesystems, and that happens while the application is built. `RegisterPlugin`
refuses it by name rather than skipping its `Configure` silently.

## Configuration

```json
{
  "elagoht/minimizer": { "html": true, "json": true, "css": true, "js": false }
}
```

`New()` enables HTML, JSON and CSS. JavaScript is off by default, because it is
where the difference between a scanner and a parser bites hardest and because the
saving is the smallest of the four — every newline is kept, so what goes is
indentation and comments.

Anything the application supplies is decoded *over* those defaults, so a section
naming only `{"js": true}` turns JavaScript on and leaves the rest as they were.
`NewWith` bypasses the defaults entirely.

## What it will not do

Everything here is a scanner, not a parser, and it only removes what cannot carry
meaning. That is a decision, not a limitation.

A minifier that is not a parser works on almost every input and corrupts the rest
silently: a regex literal containing `//`, an apostrophe inside a CSS `content`
string, a newline JavaScript needed for automatic semicolon insertion. Corrupting
one page in a thousand, with nothing logged anywhere, is far worse than saving less.

So:

- **JSON** is compacted by `encoding/json`. This is the only one of the four that is
  lossless by construction rather than by care, because the standard library knows
  the whole grammar. Invalid JSON is returned untouched.
- **HTML** keeps `<pre>`, `<textarea>`, `<script>` and `<style>` verbatim, keeps
  conditional comments, and collapses a whitespace run to a single space rather than
  removing it — a run between two inline elements renders as a space, and deleting
  it joins two words.
- **CSS** leaves strings alone and keeps the spaces around punctuation. `a :hover`
  and `a:hover` are a descendant selector and a pseudo-class, and telling a
  selector's colon from a declaration's requires knowing which side of the brace you
  are on.
- **JavaScript** keeps every newline. `return\n  x` is `return; x`, and joining
  those lines changes what the program does; deciding which newlines are safe to
  remove requires knowing where every statement ends. Strings, template literals and
  regular expression literals are tracked, so a URL inside a string does not look
  like the start of a comment.

Nothing is reordered, renamed, rewritten or re-encoded. A result that came out
larger than its input is discarded.

## Assets

Mounted files are minified by wrapping the filesystem, not the response. Mounts
serve through `http.ServeContent`, which supports `Range`: transforming bytes per
request would shift every offset, and a range request would return the wrong slice
of a file whose advertised length no longer matched. Wrapping the filesystem means
the file *is* the minified one, and ServeContent's arithmetic stays true — including
suffix ranges, which the tests pin down from the outside.

## Tests

```
go test ./...
```

The tests worth reading first are the ones that assert nothing was broken: a URL in
a JavaScript string surviving intact, `</b> world` keeping its space, a minified
document still parsing as the same JSON value.

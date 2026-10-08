# Paginate: a pagination library for path- and query-paged listings

Date: 2026-10-08
Status: approved design, pending implementation
Roadmap: [v1.0 roadmap](../plans/2026-10-03-v1-roadmap.md), phase 3, "pagination /
collections" ("export and the sitemap must know `/blog/page/2`; the core-or-plugin
question is decided while this is written").

## Motivation

`/blog/page/{n}` already works: `WithStaticParams` lists one value per page, so a
static export writes each page, and `PageURLs` and the sitemap see them. What every
application rewrites by hand is the arithmetic and the links:

- the page count;
- the slice of items for a page;
- prev, next and a numbered window (`1 … 4 5 6 … 20`);
- making sure `/blog` and `/blog/page/1` do not both serve the first page;
- for search and filter results, query-string links that keep the other
  parameters.

## Decisions

- **No core change; no plugin.** The work is pure computation and URL building, so
  it ships as a library: `github.com/Elagoht/collage-paginate`. It has no hooks and
  no registration, so it has no `collage.json` and no config schema.
- **Path paging is the export story.** The first page lives at the listing's own
  path, and the rest live under a second page registered with
  `/…/page/{n}`. A page has one pattern per locale, so the second path needs a
  second page, and both can share their fragments.
- **Query paging is server-only.** The export already warns about query-read
  pages.

## Design

### The pager

```go
type Pager struct {
	Current, Last, PerPage, Total int
	Offset, Limit                 int    // for SQL LIMIT/OFFSET or a slice
	Prev, Next                    int    // 0 when there is none
	Window                        []Slot // 1 … 4 5 6 … 20
}
type Slot struct {
	N            int
	Gap, Current bool
}

func New(total, perPage, current int) Pager
func Items[T any](items []T, p Pager) []T
func (p Pager) WithWindow(edges, around int) Pager // default: edges 1, around 2
```

- **Inputs.** `perPage < 1` panics, because that is a programming error.
  `total <= 0` gives `Last = 1` and one empty page.
- **`current` is clamped to `[1, Last]`.** `New` never fails. Deciding that an
  out-of-range page is a 404 belongs to `FromPath`.
- **`Offset` and `Limit`.** `Offset = (Current-1)*PerPage`, computed without
  overflow. `Limit` is how many items are on that page, so the last page may be
  short.
- **`Window`.** It holds `edges` pages at each end and `around` pages on each side
  of `Current`.
  - It is sorted and never repeats a number.
  - A `Gap` slot stands only where at least one number is skipped, so it is never
    `1 … 2`.
  - When there are few pages, it holds them all.
- **`Items`** returns the page's slice of an in-memory list. It returns an empty
  slice, never nil, when the page is empty.

### Path paging (static-exportable)

```go
links := paginate.Path("/blog", "/blog/page/{n}")          // URL(1)="/blog", URL(n)="/blog/page/n"
links := paginate.PathNamed(rc, "blog", "blog-page")       // locale-aware via rc.URL
params := paginate.StaticParams(count, perPage)            // for WithStaticParams: n = 2…Last
n, err := paginate.FromPath(rc, "n", last)
```

- **`count`** has the signature `func(ctx context.Context, locale string) (int, error)`.
  - Its error stops the build. This is how `WithStaticParams` already behaves.
  - When `Last <= 1`, `StaticParams` lists no pages.
- **`FromPath`** accepts only the canonical spelling of 2…`last`. `1`, `01`,
  leading zeros, signs, non-digits, overflow and out-of-range values all wrap
  `collage.ErrNotFound`, so the page answers 404. Only one URL ever serves page 1,
  and every page has one spelling.
- **`PathNamed`** builds URLs with `rc.URL(name, params)`, so locale prefixes come
  out right.

### Query paging (server only)

```go
links := paginate.Query(rc, "page")          // keeps the other query parameters; page=1 drops the key
n := paginate.FromQuery(rc, "page", last)    // invalid or out of range → 1
```

- **Other parameters** keep their order, and only the page key changes.
- **Cache keys.** The README says the page must declare
  `WithCacheParams("page", …)`, so that cached pages do not mix.

### Links for templates

```go
type Links struct {
	Prev, Next string      // "" when there is none
	Window     []LinkSlot  // Slot plus URL
}
func (l Linker) For(p Pager) Links
```

- `Path`, `PathNamed` and `Query` each return a `Linker`.
- `Links` is a plain struct, so it goes into the data handler's `collage.Data`, and
  the template type checker (v0.49+) checks `.Links.Next` and the rest.
- The README carries a template snippet for the numbered list and for the
  `rel="prev"` and `rel="next"` head links.

## Testing

- **`New`, as a table:** Offset, Limit, Prev, Next, Last, clamping, `total <= 0`,
  and the `perPage < 1` panic.
- **`Window`, as a table:** the edges, the middle, small counts, the `…` rule and
  `WithWindow`.
- **Property tests,** with random total, perPage and current:
  - the `Items` slices over every page join into the whole list, with nothing
    missing and nothing repeated;
  - `Window` is sorted and has no repeats;
  - `Offset` never overflows.
- **`FromPath`, as a table:** `2`, `01`, `1`, `0`, `-2`, `+2`, `2a`, an empty
  value, a huge number, and `last+1`.
- **`Query`:** the other parameters are kept in order, `page=1` drops the key,
  and invalid values give page 1.
- **Against a real collage app:**
  - the two-page pattern;
  - `/blog/page/1` and `/blog/page/02` answer 404;
  - an out-of-range page answers 404;
  - a static export writes `blog/page/2…N`;
  - `PageURLs` lists them;
  - `PathNamed` with a locale prefix (`/tr/blog/page/2`);
  - query paging with `WithCacheParams` keeps cached pages apart.

## Release

1. **Elagoht/collage-paginate v0.1.0,** a library that requires collage v0.57.0.
2. **CI-matrix entry,** so its tests run against every collage tag.
3. **Docs site (EN and TR):** a pagination section on the pages docs, and a
   "libraries" note in the plugins catalogue.

There is no collage release and no extension change.

## Out of scope

- Cursor-based pagination.
- Infinite scroll.
- Paging without a known total.
- One page registered under two patterns, which would be a core change.

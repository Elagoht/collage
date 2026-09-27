# Inline fragments

Date: 2026-09-27
Status: approved design, pending implementation

## Motivation

The only way to give a fragment a template today is a file:
`NewFragment(name, templatePath)`. For the small parts of a page — a table row, a
button, a form field — that is ceremony: a second file for a few lines of markup,
kept in step by hand with the data handler that feeds it. The request is to let a
fragment carry its template as a string, so a small component's handler and markup
live in one Go file.

## Goals

- `collage.NewInlineFragment(name, html string) *FragmentBuilder`: a fragment whose
  template is `html`, built with the same builder as a file fragment.
- An inline template behaves as a file template does: slots, `hoist`, every
  template function, `{{template}}` calls into file partials, registration's checks.
- Non-breaking. `NewFragment` and every file-based behaviour are unchanged.

## Non-goals

- Replacing files. Layouts and large pages stay in files; the docs steer inline
  templates towards small parts.
- File templates calling inline ones with `{{template}}`. An inline template's name
  in the set is internal.
- Editor support (HTML highlighting inside the Go string). A follow-up for the VS
  Code extension, outside this repo.

## API

```go
row := collage.NewInlineFragment("post-row", `
  <tr>
    <td>{{.Title}}</td>
    <td>{{.Date}}</td>
  </tr>`).
	WithDataHandler(loadRow).
	Build()
```

- `types.Fragment` gains `Source string`: the template text of an inline fragment.
- A fragment has exactly one of `TemplatePath` and `Source`. `Validate` reports both
  empty as the existing `ErrEmptyTemplatePath` and both set as a new
  `ErrConflictingTemplate` (re-exported from `pkg/collage`). The builders cannot
  produce the second; a hand-built `Fragment` literal can.
- An empty `html` passed to `NewInlineFragment` is recorded on the builder as
  `ErrEmptyTemplatePath`, so it surfaces through `BuildErr` and registration like
  any builder mistake.

## Engine

`internal/template.Engine` is internal, so extending it breaks nothing outside.

- New method: `AddSource(name, src string) error`. It parses `src` into the shared
  template set under `name` and keeps `(name, src)` so it survives `Reload`.
- `Reload` rebuilds the set from disk as today, then re-parses every kept source.
  Dev mode reloads before each render, so inline templates must not be lost there.
- Calling `AddSource` with a name already added and the same source is a no-op; with
  a different source it is an error (a caller bug — names are content-addressed, see
  below).
- `Lookup`, `Render`, `RenderWithFuncs`, `SlotCalls` work on the synthetic name as on
  a path. `Names()` keeps returning file template paths only (it describes the
  template directory; inline sources are not files).

## Naming

An inline fragment's template name is `inline:<fragment name>#<hash>`, where
`<hash>` is a short hex digest of the source.

- Two fragments with one name and different sources never collide.
- One source used by two fragment values parses once.
- The name is computed from the fragment; callers never write it. A helper
  `types.TemplateName(f *Fragment) string` returns `f.TemplatePath` for a file
  fragment and the synthetic name for an inline one, and every place that today
  reads `f.TemplatePath` to address the engine goes through it.

## Registration

Before `checkTemplates` runs, `prepare` walks the page's fragment tree — layout
chain, content, slot fills, fallbacks, fragment paths, and the page's not-found and
error pages — and adds every inline source to the engine. A parse error is a
registration error naming the page and the fragment:
`collage: page %q: inline template of fragment %q: %w`.

The existing checks then run unchanged against `TemplateName(f)`: the template
exists, and every slot something is bound into is one the template calls
(`ErrUnknownSlot`).

Fragments a slot resolver returns at render time are not visible at registration.
The render engine adds an inline source it has not seen before, on first render,
through the same `AddSource`; a parse error there fails that fragment under its
failure policy, as a missing template does today.

## Concurrency

`AddSource` takes the engine's write lock, as `Reload` does. Registration happens
before the server starts, so contention there is nil. The render-time path for
resolver fragments takes the lock only when the name is unknown; a known name is a
read-locked lookup.

## Surfaces

- `collage inspect`: an inline fragment reports `template: ""` and `inline: true`
  (new field on `InspectedFragment`).
- Error messages and the dev overlay name `inline template of fragment "post-row"`
  instead of a path wherever they would print the template path.
- Docs: `docs/fragments.md` gains an "Inline templates" section (when to use one,
  the backtick limit: a Go raw string cannot contain a backtick, so a template with
  a JS template literal stays in a file). CHANGELOG entry.

## Testing

- `internal/template`: `AddSource` parses and renders; a kept source survives
  `Reload`; same name + same source is a no-op, different source an error; an
  inline template can `{{template}}` a file partial.
- `internal/core`: registration adds inline sources from every part of the tree;
  a parse error names page and fragment; `ErrUnknownSlot` fires for an inline
  template that binds a slot it never calls; `ErrConflictingTemplate` for a
  hand-built fragment with both fields.
- `pkg/collage`: an inline fragment renders over HTTP (inside a file layout, with a
  slot, with hoist); two inline fragments with one name and different sources render
  their own markup; dev mode renders inline fragments across reloads; a static build
  writes a page made of inline fragments; `NewInlineFragment(name, "")` records
  `ErrEmptyTemplatePath`.
- A resolver returning an inline fragment renders it on first request.
- Verification with `go test -count=1 ./...` (the scaffold tests hide behind the
  test cache otherwise).

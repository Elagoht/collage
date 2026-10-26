# AGENTS.md for scaffolded projects — design

Date: 2026-10-26. Status: draft; open questions answered 2026-10-26, awaiting approval.

## Why

The user built Collage apps with opencode driving small, fast models
(glm-5.3-flash, deepseek-4.1-flash). The code compiled and ran; what went wrong
was the project's shape, not the API:

- several kinds (fragment, action, page) exported from one file;
- actions put in the wrong place or registered wrongly; guards forgotten;
- helpers turned into methods on pages/fragments, or on view types
  (`counterView`, a page's view struct) instead of going through the template
  function map or a helpers directory;
- `actions/funcs` used as a dumping ground for helpers — it holds the action
  handlers and nothing else;
- the project's folder structure ignored;
- CSS that must come after `{{hoist "head"}}` placed before it;
- flash messages for the auth pages put into the **master** layout's `<body>`
  before the content — every page changed, the panel broke, and the agent never
  noticed because it only looked at the auth pages;
- things Collage already ships as plugins (flash, session, validation…)
  hand-written, because the agent did not know they existed.

These models have not seen Collage in training. Everything they know comes from
this file and the code already in the project. Collage cannot fix the models; it
can hand them a short, explicit rulebook.

## Goals

1. `collage new <name> --ai` writes an `AGENTS.md` into the new project.
2. `collage ai init` writes or refreshes it in an existing project.
3. The file teaches **where things go and why**, and **what not to hand-write
   because a plugin does it**.
4. The user keeps their own project rules in the same file; Collage never
   touches them.

## Non-goals

- Mechanical enforcement (`collage check` flagging an unguarded action, mixed
  kinds in one file). Valuable, separate project.
- A Claude Code skill package (the `CLAUDE.md` bridge covers Claude Code).
- Per-project content. The file is the same in every project for a given
  Collage version — the user's call: an agent that does not know a plugin
  exists will implement it itself, so it needs the whole list, not only the
  plugins already added.
- An API reference or a copy of the docs site.

## Anti-goals

- A long file. Flash models skim; a rule past the first screens is a rule not
  read. Target: under ~200 lines for the managed section.
- Rules without reasons. Each rule carries a one-line *why*, so the model can
  act right in a case the list did not name.
- Rules that are only true of the demo. Every rule must hold for a project made
  with `--template minimal` and grown with `collage add`.
- Ever deleting or rewriting text the user wrote.

## The file

### Two sections

```markdown
<!-- collage:begin v0.77.0 — written by `collage ai init`; edits inside are lost on the next run -->
…Collage's rules and the plugin table…
<!-- collage:end -->

## Project rules

<!-- Your own rules for agents. Collage never changes anything outside the markers above. -->
```

The version in the begin marker is the Collage version that wrote it (the CLI's
own version), so a reader sees which Collage the rules describe.

### `collage ai init` behaviour

| State of `AGENTS.md` | Result |
|---|---|
| missing | managed section + empty "Project rules" heading |
| has both markers | only the text between them replaced; everything else byte-identical |
| exists, no markers | managed section **prepended**, user text kept below unchanged |
| one marker without the other, or markers out of order / duplicated | refuse, write nothing, say which marker is wrong |

Already current (same bytes) → says so and writes nothing. Run from the project
root (same project detection `collage add` uses); not in a Collage project →
error. The write must not leave a half-written file (write temp + rename, or
rename — `collage add`'s plain `os.WriteFile` does not meet this).

`collage new --ai` = the "missing" row, run as part of the scaffold, for both
templates. Without `--ai`, `new` writes no `AGENTS.md` (opt-in — the user's
call).

### Inline, not a link

Rules live inside `AGENTS.md`, not in a separate file `AGENTS.md` points to:
small models often do not follow "also read X".

### Managed-section content

Written in English (agents read English best; the user's own section can be in
any language). Imperative one-liners with a short *why*, grouped, with tiny
wrong/right snippets only where words are not enough. Content must be checked
against the code and `docs/` of the version being released — the rules below
are the substance, not final wording.

**0. Start here**
- Make new files with `collage add <kind> <[area/]name>` (page, fragment,
  action, document, layout, guard, middleware, command). It writes the files
  where they belong and registers the route. Read what it wrote, then fill it
  in. *Why:* the layout below is what `collage add` produces; hand-made files
  drift.
- Before writing a new file by hand, open an existing file of the same kind in
  this project and copy its shape.
- Run `collage check` after changes (templates, routes, slots, types).

**1. Where things go** — mirrors `collage add` (verify against `addkinds.go` /
`docs/cli.md`):
- `pages/<area>/<name>.go` — one page constructor per file.
- `fragments/pages/<area>/<name>.go` — one fragment per file, with its view type
  and data handler.
- `fragments/layouts/` — layouts. `master.go` is the site's outer layout.
- `actions/<area>.go` — action constructors (`NewAction…Build()`), grouped by
  area as `collage add action` does. Their handlers live in
  `actions/funcs/<area>.go`. Do not mix kinds: no page or fragment in an action
  file, no action in a page file.
- `actions/funcs/` — **only** `ActionHandlerFunc`s for the actions in
  `actions/`. No helpers, no shared logic.
- `guards/`, `middleware/`, `commands/`, `documents/` — one per file, as
  `collage add` writes them.
- `data/<thing>/` — state and queries (the demo's `data/count`).
- `validations/` — form validation; `utils/` — every other shared helper.
  Never helpers as methods on pages, fragments or view types.
- `routes.go` — every page, document and standalone action is registered here
  with `app.Register`. An action attached with `WithActionFor` is registered
  with its page and must not be registered again.

**2. Templates**
- View types are plain data. No methods on them for formatting or logic.
- A function a template needs that does not depend on the request → register it
  in `Config.Template.Funcs` (before `collage.New`). Anything that needs
  request state → compute it in the data handler and put it in the view.

**3. Layouts are global**
- `fragments/layouts/master.go` and its template wrap **every** page. Before
  editing them, list which pages use the layout. Something only some pages need
  (flash messages for auth, a panel sidebar) goes in a nested layout:
  `collage add layout auth --file`, then `WithLayouts(layouts.Master(),
  layouts.Auth())` on those pages.
- After changing a layout, look at a page that is *not* the one you were
  working on.

**4. Head, CSS, hoisting**
- A fragment's own stylesheet: `{{stylesheet "/static/x.css"}}` in its template
  or `rc.HoistStylesheet` in its data handler — never a `<link>` in the layout
  for one fragment.
- Titles/meta: `WithTitle`, `rc.HoistTitle`, `rc.HoistMeta`… never a second
  `<title>`.
- In a layout's `<head>`, `{{hoist "head"}}` comes after the site stylesheet,
  so a fragment's own CSS wins. Do not add stylesheet links after the hoist.
- Layouts are template files (`collage add layout <name> --file`); pages and
  fragments keep their template inline.

**5. Actions and guards**
- Every action that changes data or is for signed-in users has `WithGuard`
  (or sits on a page whose content is guarded — verify the exact rule). Pages
  and layouts for signed-in users have a guard too.
- Failure renders, success redirects (`docs/actions.md`). Use
  `collage.SafeRedirect` for any redirect target that came from the request.
- An action that changes what a cached page shows returns `InvalidateTags`.
- Never `WithoutCSRF` on a browser form.

**6. Use a plugin, don't write it** — a table keyed by the *need*, not the
plugin name, e.g.:

| You need… | Do | Don't |
|---|---|---|
| a one-time message after a redirect | `collage plugin add elagoht/flash` | your own cookie/session message code |
| login state, signed cookies | `elagoht/session` | hand-rolled signed cookies |
| form validation | `elagoht/validate` | ad-hoc checks in every handler |
| spam protection on forms | `elagoht/honeypot` | … |
| sitemap / robots / meta / feed | `elagoht/sitemap`… or `@site` / `@blog` | … |
| rate limiting, security headers, compression | `@prod` | … |
| live-updating fragments | `@live` | your own polling/websocket |
| … | … | … |

Covers the plugins of that Collage version; each row one line. Followed by:
"`collage plugin add` prints the plugin's next steps — follow them." No plugin
for the need → the app writes it, following sections 1–5.

**7. Do not**
- Do not use `any` in your code; Collage's API is typed.
- Do not edit the text between the `collage:begin` / `collage:end` markers;
  add project rules under "Project rules".

## Release and scope (*my calls*)

- Ships as the next minor before the rc; the rc has not started, so the freeze
  in `REVIEW.md` does not apply yet.
- Unrelated to the repo-root `AGENTS.md` that `REVIEW.md` asks for (rules for
  working on Collage itself). `ai init` never writes into the Collage repo.
- No staleness warning (`collage check` noticing a marker older than the CLI)
  in this work; a later candidate.

## Where the plugin table comes from

No full catalogue exists in the CLI (only `bundles.go`). The VS Code extension
and the docs site have one. For the first version the table is maintained by
hand in the template next to the CLI code, with a test that every plugin in
`bundles` appears in it (catches the obvious drift). Generating it from the
extension's catalogue is later work. — *my call*

## CLI surface

- `collage new <name> --ai` — new boolean flag, both templates, works with
  `--with`.
- `collage ai init` — new command group `ai` with one subcommand `init`, flags
  as other commands do (`-dir`?). Builder decides flag details in line with
  existing commands; `ai` with no subcommand prints its usage.
- Help/usage text, `docs/cli.md`, `CHANGELOG.md`, the docs site (EN+TR) — as
  for any CLI change. Extension snippet/command: not needed.

## Testing

- Golden test of the managed section (the template renders; markers + version).
- `ai init` table above, one test per row, including "user text byte-identical"
  and "bad markers → nothing written".
- `new --ai` writes it for minimal and demo; `new` without `--ai` does not.
- Plugin table ⊇ bundle plugins.
- Content check: a test or review step that every path/command the rules name
  exists (`collage add` kinds, directories the scaffold/add produce).
- Real-world check before release (informative, not a gate — *my call*):
  scaffold with `--ai`, give opencode + one of the flash models two tasks that
  went wrong before (auth pages with flash messages, a guarded action), once
  without and once with `AGENTS.md`. Rewrite a rule the model visibly ignored or
  misread; one round, then ship.

## Decisions (the user's, 2026-10-26)

- **Q1 — CSS order.** `{{hoist "head"}}` comes **after** the site's `app.css`,
  so a fragment's hoisted stylesheet wins over site CSS. Rule 4 says so, and
  both scaffold master layouts (minimal + demo) change: move `{{hoist "head"}}`
  below the stylesheet link. Builder: keep `<meta charset>` first in `<head>`;
  check what else in `<head>` the move affects (title from `WithTitle` is
  hoisted; icon/script order is the builder's).
- **Q2 — files.** The rule is "do not mix kinds in one file"; `actions/<area>.go`
  holding an area's actions stays.
- **Q3 — templates.** Layouts are template files under `templates/layouts/`;
  pages and fragments are inline. The scaffold follows it: the home page
  (minimal + demo) and the demo's file-based page templates become inline;
  the master layout stays a file. The rule tells agents to make a layout with
  `collage add layout <name> --file` (*my call*: `collage add layout`'s
  default stays inline — no CLI behaviour change in this work).
- **Q4 — helpers.** Shared helpers in `utils/`. Form validation in
  `validations/` (validation rules/functions, e.g. what `elagoht/validate` is
  given), not in `actions/funcs` and not in `utils/`.
- **Q5 — `CLAUDE.md`.** `ai init` and `new --ai` also write a `CLAUDE.md`
  containing `@AGENTS.md` when none exists; an existing `CLAUDE.md` is never
  touched (if it lacks `@AGENTS.md`, `ai init` prints a one-line hint).

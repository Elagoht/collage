# Plugin sealing inventory — reviewer brief

collage (github.com/Elagoht/collage) is a Go SSR framework heading to v1.0. Its core API was sealed in v0.60.x. Next, collage and its 44 plugins (github.com/Elagoht/collage-<name>, local clones at ~/Desktop/collage-<name>) go to v1.0.0-rc.1 together, then v1.0.0. A plugin at v1 promises its own API: after that, a breaking change to a plugin means a /v2 module path for that plugin. This inventory is the plugins' last chance to remove, rename, merge or reshape. READ-ONLY: do not edit, commit or push anything.

## What counts as a plugin's API
1. Every exported identifier of the plugin's importable packages (the root package and any non-internal subpackage), including methods and exported struct fields.
2. The plugin's configuration as users write it: the JSON keys of its Options/Config struct in plugins-config.json (and `json:"-"` fields that are Go-only), their defaults and meanings.
3. Its collage.json manifest (name, requires, order, setup) and its `New` signature, which `collage plugin add` writes into users' plugins.go.
4. Routes/paths it serves by default, template functions it registers (names), hoist areas, cookies it sets, CLI commands it registers.

## Inputs
- `<name>.tsv` in this directory: the root package's exported declarations (file, kind, name, detail). It does NOT list struct fields, subpackages, JSON keys, template funcs or routes — read the source for those.
- Each repo's README and collage.json.
- Usage: grep the other plugin repos (~/Desktop/collage-*/) and the docs site (~/Desktop/collage-docs/content) for each identifier before removing or renaming it.
- The core API reference: /Users/furkan/Desktop/collage/CHANGELOG.md (v0.60.0 section) and `go doc -all /Users/furkan/Desktop/collage/pkg/collage`.

## Decisions (exact words)
KEEP · RENAME → new · REMOVE (say what users do instead) · UNEXPORT · MERGE → other · CHANGE (say how) · ASK (a real trade-off for the user; one line with the options). Be decisive; ASK sparingly.

## Rules to apply consistently across plugins
- Convention today: `const Name = "elagoht/<name>"`, `New(...)`, often `NewWith(cfg)`, `Options` (some use `Config`), `Version()`. Propose ONE convention for all plugins (constructor names and the options type name) and flag every plugin that deviates.
- "Exported because it was convenient" is a reason to remove; something another plugin imports is not removable without changing that plugin too (say which).
- Config keys: consistent casing and naming across plugins (e.g. `baseURL` everywhere), no two keys for one fact, booleans phrased so the zero value is the default.
- No `any`/`interface{}` in exported signatures where a type is possible.
- Duplicated helpers that now exist in core (e.g. a plugin's own origin/BaseURL parsing, its own route-kind strings — core v0.61.0 adds collage.RouteKind*/Stage*/Built* constants and collagetest.NewSite) are CHANGE → use core.

## Output
Write to the output file named in your prompt, Markdown:
1. One section per plugin: a table `| item | kind | decision | detail | why | breaks (other plugins / docs) |` covering every exported identifier (fields only when non-KEEP), every config key that deserves a non-KEEP decision, and any route/template-func/cookie/command naming issue. Then one line: "plugin verdict: unchanged / patch-level / API changes".
2. "Cross-cutting": at most 10 bullets — conventions you propose, with the list of plugins each one touches.
3. "Bugs found in passing": at most 8.
Reply to the caller with only a 6-line summary (plugins with API changes, count per decision, top 3 findings).

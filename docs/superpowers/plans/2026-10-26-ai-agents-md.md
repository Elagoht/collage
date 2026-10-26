# AGENTS.md for scaffolded projects — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `collage new --ai` and `collage ai init` write an `AGENTS.md` (Collage-managed section + the user's own rules) and a `CLAUDE.md` bridge into a project; the scaffold follows the same rules (hoist after `app.css`, inline pages/fragments, file layouts).

**Architecture:** The managed text is one embedded file, `internal/cli/ai/AGENTS.md`, with `@@VERSION@@` substituted. One function merges it into whatever `AGENTS.md` exists (markers), one writes both files atomically; `ai init` and `new --ai` both call that writer. Scaffold changes are template edits plus test file lists.

**Tech Stack:** Go stdlib, existing `internal/cli` patterns (`flag.FlagSet`, `splitPositional`, `testCLI()`).

**Spec:** `docs/superpowers/specs/2026-10-26-ai-agents-md-design.md`

## Global Constraints

- Branch `feat/ai-agents-md` in worktree `~/Desktop/collage-ai`. Never `git add -A`; add paths by name.
- No `any` in Go code.
- Tests: `go test -count=1 ./internal/cli/...` (the scaffold-compiles test is cached otherwise). Before finishing: `go vet ./...`, `staticcheck@v0.7.0 ./...`, `gofmt -l .` empty.
- Begin marker exactly: `<!-- collage:begin v<version> — written by \`collage ai init\`; edits between these markers are lost the next time it runs -->`; end marker `<!-- collage:end -->`. Detection matches the prefixes `<!-- collage:begin` and `<!-- collage:end -->`, line-based, CRLF accepted.
- User text outside the markers is preserved byte-for-byte.
- `CLAUDE.md` content when created: `@AGENTS.md\n`. An existing `CLAUDE.md` is never written.
- Managed section ≤ 200 lines.
- No release, tag or push without the user.

## Review Focus

1. `collage new --ai site` and `collage new site --ai` both scaffold `site` (bool flag must not eat the name) — Task 3 test.
2. `new --ai --force` into a dir with the user's `AGENTS.md` keeps their text and does not list it as overwritten — Task 3 test.
3. `AGENTS.md` written on Windows (CRLF) with markers is still recognised, not prepended twice — Task 1 test.
4. Running `ai init` twice is a no-op the second time ("already current"), file unchanged — Task 2 test.
5. A user's `AGENTS.md` that merely *mentions* `collage:begin` inside text (not at line start) is not mistaken for a marker — Task 1 test.

---

### Task 1: Managed text, renderer and merge

**Files:**
- Exists (drafted, review with user): `internal/cli/ai/AGENTS.md`
- Create: `internal/cli/aimd.go`, `internal/cli/aimd_test.go`

**Interfaces:**
- Produces:
  - `var ErrAgentsMarkers = errors.New("collage: AGENTS.md markers")`
  - `func managedSection(version string) []byte` — the embedded file with `@@VERSION@@` → version; ends with `\n`.
  - `func mergeAgents(existing []byte, section []byte) ([]byte, error)` — `existing == nil` means no file.
  - `const projectRulesStub = "\n## Project rules\n\n<!-- Your own rules for agents. Collage never changes anything outside the markers above. -->\n"`

- [ ] **Step 1: Failing tests** in `aimd_test.go`:
  - `TestManagedSection_Version`: `managedSection("1.2.3")` starts with `<!-- collage:begin v1.2.3 —`, ends with `<!-- collage:end -->\n`, contains no `@@`.
  - `TestManagedSection_LineBudget`: ≤ 200 lines.
  - `TestManagedSection_NamesRealThings`: every kind in `addKinds` appears as `` `<kind>` ``; every bundle name in `bundles` appears as `` `@<name>` ``; every plugin in every bundle appears (as `` `owner/name` `` or covered by its bundle row — assert each bundle's plugins are named in the file or the bundle is); text contains `collage plugin add`, `collage check`, `validations/`, `utils/`, `actions/funcs/`.
  - `TestMergeAgents` table:
    | existing | want |
    |---|---|
    | `nil` | `section + projectRulesStub` |
    | `"old section\n"` wrapped in markers + `"\n## Project rules\nmine\n"` | new section + `"\n## Project rules\nmine\n"` byte-identical tail |
    | `"# Mine\nrule\n"` (no markers) | `section + "\n" + "# Mine\nrule\n"` |
    | markers with CRLF line endings | replaced; text outside kept as is |
    | text before the begin marker | kept before |
    | only begin / only end / end before begin / two begins | `ErrAgentsMarkers` (wrapped, message says which) |
    | a line `see <!-- collage:begin` mid-line, no real markers | treated as no markers (prepend) |
- [ ] **Step 2:** `go test -count=1 -run 'ManagedSection|MergeAgents' ./internal/cli/` → FAIL (undefined).
- [ ] **Step 3:** Implement in `aimd.go`: `//go:embed ai/AGENTS.md`; markers found by scanning lines (`strings.HasPrefix(strings.TrimRight(line, "\r"), ...)`); replace from the begin line through the end line inclusive.
- [ ] **Step 4:** Same command → PASS.
- [ ] **Step 5:** Commit `internal/cli/ai/AGENTS.md internal/cli/aimd.go internal/cli/aimd_test.go` — `feat(cli): AGENTS.md managed section and merge`.

### Task 2: Writing the files; `collage ai init`

**Files:**
- Modify: `internal/cli/aimd.go`, `internal/cli/cli.go` (builtins, `Run` switch, `printCommandHelp`, `ErrReservedCommandName` doc comment)
- Create: `internal/cli/ai.go`, `internal/cli/ai_test.go`

**Interfaces:**
- Consumes: Task 1.
- Produces:
  - `type aiWritten struct{ Agents, Claude string }` — each `"created"`, `"updated"`, `"current"` or `""` (not touched); `Claude` also `"exists"` when a `CLAUDE.md` was left alone.
  - `func writeAIFiles(dir, version string) (aiWritten, error)`
  - `func writeFileAtomic(path string, data []byte, perm fs.FileMode) error` — temp file in the same dir, `Sync`, `Rename`; temp removed on error.
  - `func (c *CLI) runAI(args []string) int`; `const aiUsage`.

- [ ] **Step 1: Failing tests** in `ai_test.go`:
  - `TestWriteAIFiles_Fresh`: empty dir → `AGENTS.md` = section+stub, `CLAUDE.md` = `@AGENTS.md\n`, result `{created, created}`.
  - `TestWriteAIFiles_Twice`: second call → `{Agents: "current", Claude: "exists"}`, both files' bytes unchanged.
  - `TestWriteAIFiles_KeepsClaude`: existing `CLAUDE.md` "mine" untouched, `Claude == "exists"`.
  - `TestWriteAIFiles_BadMarkers`: returns `ErrAgentsMarkers`, `AGENTS.md` bytes unchanged, no `CLAUDE.md` created.
  - `TestRun_AI_Init`: in a temp project made by `new` (minimal, no `--ai`), `ai init -dir <p>` exits 0, prints `AGENTS.md: created` and `CLAUDE.md: created`.
  - `TestRun_AI_NotAProject`: `ai init -dir <empty temp>` exits 1, stderr names go.mod.
  - `TestRun_AI_NoSubcommand`: `ai` exits 2, usage on stderr; `help ai` exits 0 with usage.
  - `TestRun_AI_ClaudeHint`: existing `CLAUDE.md` without `@AGENTS.md` → stdout has one hint line `CLAUDE.md does not import AGENTS.md; add a line "@AGENTS.md" to it`.
- [ ] **Step 2:** `go test -count=1 -run 'WriteAIFiles|Run_AI' ./internal/cli/` → FAIL.
- [ ] **Step 3:** Implement. `ai init [-dir path]`; project check = `readProject(dir)` (go.mod with a module). `"ai"` added to `builtins` as `{"ai", "Write or refresh AGENTS.md, the rules for coding agents: ai init"}` after `plugin`. Version from `Version`.
- [ ] **Step 4:** Same → PASS; `go test -count=1 ./internal/cli/` all PASS (existing usage/help golden tests may list builtins — update them).
- [ ] **Step 5:** Commit — `feat(cli): collage ai init writes AGENTS.md and CLAUDE.md`.

### Task 3: `collage new --ai`

**Files:** Modify `internal/cli/new.go` (flag, `newUsage`, after `writeScaffold`), `internal/cli/new_test.go`.

**Interfaces:** Consumes `writeAIFiles`.

- [ ] **Step 1: Failing tests:**
  - `TestRun_New_AI`: `new site --ai -dir <t>` → `AGENTS.md`, `CLAUDE.md` exist; for both `--template minimal` and `demo`.
  - `TestRun_New_AIBeforeName`: `new --ai site -dir <t>` → project `site` scaffolded (module `site`), files present.
  - `TestRun_New_NoAI`: without `--ai` neither file exists.
  - `TestRun_New_AIForceKeepsUserRules`: dir with `AGENTS.md` "# Mine\n" → `new site --ai --force -dir <t>`; file ends with `# Mine\n`, starts with the begin marker; stdout's "Overwrote" line does not name `AGENTS.md`.
- [ ] **Step 2:** `go test -count=1 -run 'Run_New_AI|Run_New_NoAI' ./internal/cli/` → FAIL.
- [ ] **Step 3:** `ai := fs.Bool("ai", false, "write AGENTS.md and CLAUDE.md for coding agents")` (not in `newValueFlags`); after scaffolding, `writeAIFiles(targetDir, Version)`; print `Wrote AGENTS.md for coding agents` (or its error, exit 1). Add `[--ai]` to `newUsage` with one line of description.
- [ ] **Step 4:** PASS.
- [ ] **Step 5:** Commit — `feat(cli): collage new --ai`.

### Task 4: Scaffold follows the rules

**Files:**
- Modify: `scaffold/minimal/templates/layouts/master.html.tmpl`, `scaffold/demo/templates/layouts/master.html.tmpl` — `{{hoist "head"}}` moved below the `app.css` `<link>` (charset/viewport stay first; in demo it goes after the last `<link>`/`<script>`).
- Move `scaffold/common/fragments/pages/home.go.tmpl` → `scaffold/minimal/fragments/pages/home.go.tmpl` (inline `<h1>Hello from {{.Name}}</h1>`) and `scaffold/demo/fragments/pages/home.go.tmpl` (inline, body of today's `demo/templates/pages/home.html.tmpl`).
- Modify `scaffold/demo/fragments/pages/demo/features.go.tmpl` → inline, body of `features.html`.
- Delete `scaffold/minimal/templates/pages/home.html`, `scaffold/demo/templates/pages/home.html.tmpl`, `scaffold/demo/templates/pages/features.html`.
- Copy: both demo templates' "Edit `templates/pages/…` and reload … read from disk on every request" → say the page's markup is in `fragments/pages/….go` and `collage dev` rebuilds on save; the layout in `templates/layouts/master.html` is still read from disk. Update the demo home's tree listing (`@@NAME@@/` block) to match. Comments in `home.go.tmpl`/`features.go.tmpl` stop naming `templates/pages/`.
- Tests: `internal/cli/new_test.go` expected file lists (`TestRun_New_ScaffoldsExpectedFiles`, `TestRun_New_Minimal` ~line 350). Do not touch the synthetic paths in `dev_test.go`.
- `templates/` must still exist in both variants (`embed.go.tmpl` embeds `all:templates`) — it does, via `templates/layouts/`.

- [ ] **Step 1:** Update the expected file lists; add `TestScaffold_HoistAfterStylesheet`: for each variant's rendered master, index of `{{hoist "head"}}` > index of `app.css`.
- [ ] **Step 2:** `go test -count=1 -run 'Run_New|Scaffold' ./internal/cli/` → FAIL.
- [ ] **Step 3:** Make the template changes above.
- [ ] **Step 4:** `go test -count=1 ./internal/cli/...` PASS, including `TestRun_New_Scaffold_Compiles` and the demo's own `main_test.go` (it runs inside the compile test — confirm the demo pages still render with their text).
- [ ] **Step 5:** Commit — `feat(scaffold)!: pages inline, layouts in files, hoist after the site stylesheet`.

### Task 5: Docs and changelog

**Files:** `docs/cli.md` (new `## \`collage ai\`` section after `collage plugin`; `--ai` in `collage new`; lines ~183/191 and the demo tree in "What `collage new --template demo` gives you"), `CHANGELOG.md` (`## Unreleased`: Added `collage ai init`, `new --ai`; Changed: scaffold pages inline, hoist order), scaffold `README.md.tmpl`s if they mention `templates/pages`.

- [ ] **Step 1:** `grep -rn "templates/pages" docs internal/cli/scaffold README.md` → each hit fixed or still true (`collage add --file` lines stay).
- [ ] **Step 2:** Write the docs; `go test -count=1 ./...` PASS; vet/staticcheck/gofmt clean.
- [ ] **Step 3:** Commit — `docs: collage ai and the scaffold's new shape`.

### After the plan (user, not the executor)

- Review `internal/cli/ai/AGENTS.md` wording.
- Real-world check on the Mac mini: scaffold with `--ai`, opencode + glm-5.3-flash / deepseek-4.1-flash, the flash-on-auth-pages and guarded-action tasks, with and without `AGENTS.md`.
- Release (version, tag, docs site EN+TR, extension if snippets mention `templates/pages`) — only on the user's go.

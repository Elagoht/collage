# Plugin sealing — implementer brief

You bring a batch of collage plugin repos to the v1 plugin conventions, one repo at a time, and leave each one committed locally (not pushed, not tagged) for review. collage v0.61.0 is tagged and on the proxy.

## Read first
- The rules: `/Users/furkan/Desktop/collage/docs/superpowers/specs/plugin-seal/conventions.md` — binding.
- Your repos' rows: `/Users/furkan/Desktop/collage/docs/superpowers/specs/plugin-seal/out-1.md` … `out-4.md` (each plugin has a section with a decision per item, plus "Bugs found in passing"). Where a row conflicts with conventions.md, conventions.md wins; ASK rows were settled in conventions.md (rules 1–4 by the user, 5–14 by the controller).
- What core v0.61.0 gives you: `/Users/furkan/Desktop/collage/CHANGELOG.md` (v0.61.0 section): `collage.Duration`, `collage.IsHTTPS`, strict plugin keys, `collagetest.NewSite`, `collage.RouteKind*`/`Built*`/`Stage*`, `DocumentBuilder.WithTimeout`, `ParseOrigin` refusing hostless origins.

## For each repo, in the order given
1. `cd ~/Desktop/collage-<name>`; `git status --short` must be empty (else skip it and report); `git checkout main` (or the default branch); `git pull -q --ff-only`.
2. `GOPROXY=direct GONOSUMDB=github.com/Elagoht go get github.com/Elagoht/collage@v0.61.0`; if your batch note lists dependency plugins with new tags, `go get` those too.
3. Apply every non-KEEP row of this plugin's section and every convention that touches it. Fix every bug the inventory lists for it, each with a test that fails first. Keep changes to what the rows, conventions and bugs ask; no unrelated refactors.
4. Tests: new and rewritten tests use `collagetest.NewSite`; convert the repo's main test-site helper to NewSite when it is a straight replacement. Use `collage.Duration` and the core constants in tests. Every renamed or removed config key gets a test that the old key now fails at Init (strict keys do this; the test pins it).
5. Manifest: regenerate collage.json from the new Options with the extension's generator, scoped to this repo:
   ```sh
   SG=$(mktemp -d); ln -s ~/Desktop/collage-<name> "$SG/collage-<name>"
   (cd ~/Desktop/collage-snippets-highlighter/tools/schemagen && go run . -src "$SG" -out "$SG/schema.json" -manifests)
   ```
   (The generator in that repo knows `collage.Duration` from a local commit; don't commit anything in the extension repo.) Then add or fix `setup` and `requires` per conventions rule 12, by hand (the generator keeps them).
6. README: every key, constructor and example matches the code; add a short "Changed in vX.Y.0" section listing each renamed/removed key and API with its replacement. Update the "Requires collage" line to v0.61.0.
7. Bump the version (minor for API changes, patch otherwise — conventions "Release of each plugin"); update `Version()`/the version const and any test pinning the old version string.
8. `gofmt -l .` empty, `go vet ./...`, `go test -race -count=1 ./...`, and the contract check from the collage checkout: `cd /Users/furkan/Desktop/collage && go run ./cmd/collage plugin check -run ~/Desktop/collage-<name>` (skip for collage-paginate, a library).
9. Commit (files added by name, never `git add -A`), message `feat!: v1 conventions, vX.Y.Z` (or `fix:` for a patch) with a body listing the API changes, ending with a blank line and `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`. Do NOT push and do NOT tag: a reviewer reads the batch first.
10. Append to your batch report: repo, old tag → planned new version, API changes, bugs fixed (with test names), test output summary, contract check result, anything you could not do and why.

Never use `any`/`interface{}` in new exported signatures where a type is possible (existing template-function signatures that must take any are exempt; say so). Do not dispatch subagents. If a row needs a design decision that conventions.md and the rows don't settle, make the smallest reasonable choice, note it under "Rulings" in your report, and continue.

Reply to the caller with only: status, repo → planned version list, rulings you made, repos skipped and why, the report path.

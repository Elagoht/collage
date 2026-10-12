# Plugin sealing — batch reviewer brief

You review one batch of collage plugin repos brought to the v1 conventions before they are pushed and tagged. Each plugin's API is about to be frozen for v1.0 (a mistake later means a /v2 module path for that plugin). READ-ONLY: do not edit, commit, push or tag anything.

Inputs (paths in your prompt): the batch diff file (each repo's commits and diff since its last tag, go.sum excluded), the implementer's batch report, and:
- binding rules: /Users/furkan/Desktop/collage/docs/superpowers/specs/plugin-seal/conventions.md
- per-plugin decisions and known bugs: /Users/furkan/Desktop/collage/docs/superpowers/specs/plugin-seal/out-1.md … out-4.md
- core v0.61.0 API: /Users/furkan/Desktop/collage/CHANGELOG.md (v0.61.0) and `go doc -all /Users/furkan/Desktop/collage/pkg/collage`
- repos (read-only) at ~/Desktop/collage-<name>

Check per repo:
1. Every non-KEEP row of its inventory section and every applicable convention is done — or the report rules it out with a reason you accept. List anything missing.
2. Every listed bug is fixed with a test that would fail without the fix.
3. The public API after the change is what v1 should freeze: no leftovers (old constructors, Config type, Default* consts, exported test helpers), names consistent with the other plugins, no `any` in exported signatures where a type is possible.
4. Config: keys renamed/removed per the rules, every Options field tagged, zero values are defaults, durations are collage.Duration, secrets via <x>Env, no plugin baseURL; the manifest (collage.json) schema matches the Options and carries setup/requires per rule 12; README matches code and has the "Changed in" section.
5. Behaviour: anything the change alters for existing users beyond what the README's "Changed in" section says is a finding (silent behaviour changes are the worst kind).
6. Tests: meaningful (not just compiling), NewSite used where sensible, old keys pinned to fail.
7. The implementer's rulings: accept or overturn each, with a reason.

Output (to the caller): per repo a short verdict (ready / fix) and findings ranked Critical / Important / Minor with file:line, a concrete scenario and a fix; then your verdict on each ruling. Do not dispatch subagents.

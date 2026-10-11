# `collage plugin`: add and remove plugins from the command line

Date: 2026-10-11
Status: approved design, pending implementation
Roadmap: [v1.0 roadmap](../plans/2026-10-03-v1-roadmap.md). This work is not a
roadmap item. The user asked for it before phase 5 (sealing). It defines a
contract every plugin repo must meet, so it has to land before the API is
frozen.

## Motivation

Adding a plugin today takes four manual steps:

1. run `go get github.com/Elagoht/collage-<name>`;
2. add an import and a constructor call to `Config.Plugins` in `main.go`;
3. add a section to `plugins-config.json`;
4. remember the plugin's place in the list.

Removing one takes the same steps in reverse. The goal is one command each way,
for our plugins and for anyone else's that meet a published rule.

## Decisions

- **The CLI writes plugins into a file it manages, `plugins.go`.** It never
  edits `main.go`. `main.go` says `Plugins: plugins()` once.
- **A short name is the plugin's name.** `elagoht/consent` resolves to
  `github.com/elagoht/collage-consent`. A full module path is used as written,
  on any host.
- **Constructors are not unified.** The CLI reads the downloaded package's `New`
  signature from source and writes a call that fits. No plugin's API breaks.
- **Configuration starts as an empty object.** `"elagoht/consent": {}` means
  "defaults". A required field that is missing is reported by the plugin's
  `Init` at startup. The editor extension completes the keys inside the object.
- **The rule is checkable.** `collage plugin check` checks it. Every plugin in
  our CI matrix passes it on every run.

## Design

### 1. Commands

```
collage plugin add <ref>[@version]
collage plugin del <ref>
collage plugin check [dir]
```

`<ref>` takes one of two forms:

- **Short:** `owner/name`, two segments with no dot in the first. It resolves to
  `github.com/<owner>/collage-<name>`.
- **Full:** a module path whose first segment has a dot
  (`github.com/kisi/repo`, `gitlab.com/x/y`). It is used as written.

`@version` is anything `go get` accepts (`@v0.1.2`, `@latest`). The default is
`@upgrade`: it never moves a module down (from a prerelease above the latest
release, say), and for a module `go.mod` does not have yet it is `@latest`.

All three commands run in the project directory, the one that holds `go.mod`.
`add` and `del` refuse to run outside a module.

### 2. The plugin repo contract

A module is a collage plugin that `add` accepts when all of these hold:

1. **A manifest exists.** The module root has a `collage.json` that decodes
   strictly:
   - every top-level key must be one the manifest schema
     (`collage-plugin-manifest.schema.json`, shipped with the editor extension)
     allows;
   - the fields the CLI uses (`name`, `requires`, `order`, `setup`) must be
     well formed.

   Full JSON Schema validation stays with the extension. The CLI uses only
   the standard library.
2. **The name is well formed.** The manifest's `name` is `owner/name`, both
   segments `[a-z0-9][a-z0-9-]*`. A short ref must match it: `add elagoht/consent`
   needs `name: "elagoht/consent"`. The name is also what the plugin's `Name()`
   returns. The CLI cannot see `Name()`, so `check` verifies it only when run
   in the repo with `-run` (see §6).
3. **The plugin package is the module root.** Its import path is the module path.
4. **`New` can be called without configuration.** It is a function, not a
   method, and its parameters are one of:
   - none, or only a variadic one: the call is `pkg.New()`;
   - exactly one parameter: the call passes its zero value. That is `pkg.T{}` for
     a struct type defined in the package (`sitemap.New(sitemap.Options{})`),
     and `nil` for a pointer, interface, slice, map or func type
     (`uploads.New(nil)`).

   Other shapes are refused, such as two parameters, a basic type or an
   unexported struct type, which another package cannot write. New must return
   exactly one result. A module root in `package main` is refused, since it
   cannot be imported. New must succeed with that call; Configure or Init may
   refuse a required field an empty section lacks, which is the startup error
   the user then sees. `check -run` tests the call (see §6).
5. **The module is released.** The resolved version is a semver tag. A
   pseudo-version (`v0.0.0-2026…-abcdef`) is refused, because an untagged repo
   gives no stable version to pin.

The manifest gains three optional fields:

- **`requires`:** a list of plugin names that must also be in the project. For
  example, websocket requires `["elagoht/live"]`.
- **`order`:** an integer, default 0. Lower values sit earlier in
  `Config.Plugins`, which means outermost as middleware. Ours:
  - health −30: before anything that refuses or answers a request;
  - fail2ban −25: right after health;
  - compress −20: before any plugin that rewrites response bodies;
  - devtoolbar 100: last, so it sees the other plugins' findings.

  Every other plugin is 0.
- **`setup`:** a short Markdown note that `add` prints after it succeeds, for
  what only Go can supply. For example, feed's note says its feeds are passed to
  `feed.New(...)` in `plugins.go`.

The schema generator in the extension (`schemagen -manifests`) keeps these three
fields when it regenerates a manifest. They are written by hand in each plugin
repo.

### 3. `plugins.go`

```go
// Code generated by collage plugin, and yours to edit: reorder the list, or give
// a plugin its options in Go. "collage plugin add" and "del" add and remove
// whole entries and leave the rest alone.

package main

import (
	"github.com/Elagoht/collage/pkg/collage"

	consent "github.com/Elagoht/collage-consent"
	devtoolbar "github.com/Elagoht/collage-devtoolbar"
)

// plugins are the application's plugins, in order: the first is the outermost
// middleware. Their settings are in plugins-config.json.
func plugins() []collage.Plugin {
	return []collage.Plugin{
		consent.New(),
		devtoolbar.New(),
	}
}
```

**How the CLI edits it.**
- The CLI parses the file with `go/parser` and edits it with `go/ast`, then
  formats it with `go/format`. The file has a shape the CLI defined, so this is
  safe.
- Every entry is a call expression `<ident>.New…(…)` or `<ident>.NewWith(…)`
  whose ident names an import. The entry belongs to that import's module.
- Changes the user makes are kept: arguments, a different constructor, the
  order, comments, other statements. `add` only inserts one entry and its
  import. `del` only removes the entries and the import of one module.

**Package and location.**
- The file is written into the directory given by `-dir` (default `.`).
- That directory must hold a non-test `package main` file and the project's
  `go.mod`.

**Imports.**
- An import always carries an explicit name, so the call and the import read
  alike.
- The name is the package name declared in the plugin's source.
- If that clashes with another import in the file, a name the file declares, or
  a package-level name another file of the package declares, the name becomes
  `<owner><Name>`, for example `elagohtConsent`. A further clash appends a number.

**Order.**
- The new entry goes before the first existing entry whose module's manifest
  has a greater `order`. Otherwise it goes last.
- Entries the CLI cannot attribute (a plugin built by hand, without a manifest)
  count as order 0.
- The CLI never moves an existing entry. A user's reordering stands.

**When `main.go` doesn't call `plugins()`.** The case: `plugins.go` is new and no
file in the package calls `plugins()`. `add` still succeeds, and prints:

```
plugins.go is new: give it to collage.New once —
	Plugins: plugins(),
```

The scaffold (`collage new`, both templates) ships an empty `plugins.go` and
`Plugins: plugins()`.

### 4. `add`

1. **Resolve the ref.** A short ref becomes `github.com/<owner>/collage-<name>`.
2. **Back up `go.mod` and `go.sum`** (in memory).
3. **Run `go get <path>@<version>`.**
   - If the failure says the module declares a different path ("module declares
     its path as: X"), and X matches the requested path case-insensitively, retry
     once with X.
   - Any other failure restores the backup and is reported with go's own message.
4. **Locate and check the module.** Locate it with `go list -m -json <path>`,
   which gives `Dir` and `Version`. Check the contract (§2) against `Dir`.
   - On any failure, restore `go.mod` and `go.sum` and print the rule that
     failed. For example: `elagoht/foo: no collage.json at the module root; see
     https://collage.furkanbaytekin.dev/en/docs/plugin-authors/`.
5. **`requires`.**
   - Each required plugin missing from `plugins.go` is added first, by the same
     procedure. It is resolved by its name as a short ref.
   - A cycle is an error.
   - The output lists each plugin added.
6. **Already present?** If the module already has an entry in `plugins.go`,
   `add` does not add a second one. It reports `elagoht/consent is already
   added; now at v0.1.2` when `go get` changed the version, or `already added`
   when it didn't.
7. **Edit `plugins.go`** as in §3.
8. **Edit `plugins-config.json`.**
   - If the file is missing, create it as `{ "elagoht/consent": {} }`.
   - If it has a section under the plugin's name, leave it alone.
   - Otherwise add `"elagoht/consent": {}` as the last member.
   - The edit is textual. The existing text, its formatting and its key order are
     kept, and the new member copies the indentation of the existing members.
   - A file that is not a JSON object is an error, and nothing is written.
9. **Run `go mod tidy`.** If it fails, restore `go.mod`, `go.sum`,
   `plugins.go` and `plugins-config.json`, and report the failure.
10. **Print the result:**

   ```
   added elagoht/consent v0.1.2
     plugins.go           consent.New()
     plugins-config.json  "elagoht/consent": {}
   ```

   followed by the manifest's `setup` note, if it has one.

The order of writes, and the restore on failure, make `add` all-or-nothing.
Nothing is written until the module has passed its checks.

### 5. `del`

1. **Find the module.**
   - A full ref names the module.
   - A short ref is matched against each entry's module in two ways: by the
     manifest name, read from the module cache, and by the conventional path,
     compared case-insensitively.
   - No match is an error that lists the plugins `plugins.go` has.
2. **Check dependents.** If another entry's manifest `requires` this plugin,
   refuse: `elagoht/websocket requires elagoht/live; remove it first`.
   - A module the module cache lacks is downloaded to read its manifest.
   - If another entry's `collage.json` cannot be read, refuse, naming the module
     and the reason: nothing proves it does not require this plugin. A module
     with no `collage.json`, or a package of the project's own module, requires
     nothing.
   - A full ref whose own `collage.json` cannot be read is refused too, since
     the name of its config section is unknown.
3. **Edit the files.**
   - Remove the module's entries and import from `plugins.go`.
   - Remove its section from `plugins-config.json`, also as a textual edit that
     keeps the rest.
   - If the removed section was not `{}`, print it, so settings are not lost
     silently.
4. **Run `go mod tidy`.** It drops the requirement once nothing imports the
   module. If it fails, the edits are restored.

### 6. `check`

`collage plugin check [dir]` checks the contract (§2) on a plugin repo on disk.
`dir` defaults to `.`.
- Rules 1–4 are checked statically.
- Rule 5 (a release tag) is checked only with `-release`, which needs git: it
  reads the tags at `HEAD`.
- `-run` also builds a throwaway program with `go run` against the repo. The
  program compiles the exact call `add` would write, in the same
  `collage.Plugin` context `plugins.go` gives it, runs it, and checks that
  `Name()` equals the manifest's `name`. It fails on a build error, a panic in
  `New` or a `Name()` mismatch. It does not call `collage.New` or `Start`, so it
  neither configures nor initialises the plugin: a required setting that an
  empty config section lacks is the plugin's own startup error, not a contract
  failure. The program imports only the checked plugin and collage, so required
  plugins play no part. `go.mod`, `go.sum` and `go.work.sum` are put back
  afterwards, and the program's directory is removed.
- Our CI matrix runs `collage plugin check -run` on every plugin's latest tag.
  `collage-paginate` is a library, not a plugin, and is skipped.

### 7. Our plugins

- **feed** already fits: `New(feeds ...Feed)` is called as `feed.New()`. Its
  manifest gains a `setup` note, and `Init` with no feeds logs one Warn and serves
  nothing.
- **uploads** fits as `uploads.New(nil)`, which already uses the disk store under
  `dir`. It needs no change beyond the `check -run` pass.
- **websocket**: `New(lv *live.Plugin)` with `nil` fails today. Fix:
  - With a nil `lv`, `Configure` finds the live plugin among the application's
    plugins through a new core helper:
    `collage.FindPlugin[T collage.Plugin](host) (T, bool)`.
  - It then calls `UseTransport`, which live needs before the application is
    built.
  - The helper sees every plugin in the application's list, configured or
    initialised or not, from both `Configure` and `Init`.
  - The manifest gets `requires: ["elagoht/live"]` and `order: -1`, so
    websocket is configured before live.
- **Ordering:** health, fail2ban, compress and devtoolbar gain their manifest
  `order`.
- **Every plugin** must pass `check -run`. A plugin that fails is fixed in a
  patch release. The seven manifests that drifted (see the consent release)
  are regenerated in the same pass.

### 8. Core

- **The `plugin` command** lives in `internal/cli`. The contract check
  (`internal/cli/plugincontract`) is shared by `add` and `check`.
- **`collage.FindPlugin`** is additive. Phase 5 will place it among the
  optional host capabilities.
- **The scaffold** gains `plugins.go` and `Plugins: plugins()`.

## Testing

- **A fake module proxy.**
  - Tests write modules into a `GOPROXY=file://…` directory: `.info`, `.mod` and
    a `.zip` built with `archive/zip` in the module zip layout.
  - They set `GOFLAGS=-mod=mod`, `GONOSUMDB=*` and
    `GOSUMDB=off`, and a private `GOMODCACHE`, so nothing reaches the network
    or the user's module cache.
  - The fixtures cover:
    - a valid plugin with `New()`;
    - one with `New(Options)`;
    - one with `New(store Store)`;
    - a variadic one;
    - one with two parameters;
    - one without a manifest;
    - one whose manifest name mismatches;
    - an untagged one;
    - a mixed-case module path;
    - a pair where one `requires` the other.
- **`add`:**
  - the generated `plugins.go` compiles (`go build` on a scaffolded project);
  - a second `add` is a no-op;
  - import names clash;
  - order placement works, including around a hand-reordered list;
  - user edits to arguments survive;
  - `plugins-config.json`: created, appended with the existing formatting kept,
    an existing section left alone, a non-object refused;
  - every failure restores all four files byte for byte.
- **`del`:**
  - it finds a module by short ref, by full ref, and with a mixed-case path;
  - a dependent blocks it;
  - a non-empty section is printed;
  - `go mod tidy` drops the requirement.
- **`check`:** one case per rule, `-release` against a git repo with and without
  a tag, and `-run` against a fixture plugin.
- **The scaffold:** a new project builds and runs with an empty `plugins()`.

## Release

1. **collage v0.58.0:** the `plugin` command, `FindPlugin`, the scaffold and the
   manifest schema in the CLI.
2. **Plugins:**
   - websocket (FindPlugin, `requires`);
   - feed (setup note, no-feeds Warn);
   - health, fail2ban, compress, devtoolbar (`order`);
   - the seven drifted manifests;
   - any plugin that fails `check -run`.

   Each gets a patch release, or a minor release where behaviour changes.
3. **CI:** the matrix runs `collage plugin check -run`.
4. **Extension:** the manifest schema gains `requires`, `order` and `setup`;
   schemagen keeps them.
5. **Docs site (EN and TR):**
   - the CLI page;
   - a new "Writing a plugin" page with the contract;
   - the plugins page leads with `collage plugin add`.

## Out of scope

- **A plugin registry or search** (`collage plugin search`). GitHub is the
  registry.
- **Upgrading all plugins at once.** Running `add` again on a plugin upgrades it.
- **Signing or verifying third-party plugins** beyond the Go checksum database.
  `go get` already checks sums, and the docs say a plugin is code you run.
- **Editing `main.go`.**

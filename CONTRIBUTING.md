# Contributing

Thank you for looking. Collage tries to stay small, so the best contribution is
often a bug report with the smallest program that shows the bug.

## Before you start

For anything larger than a fix, open an issue first. A feature is weighed
against the cost of every user having to learn it; many good ideas are better as
a plugin, and the issue is where that gets decided before you have written it.

## What a change needs

- **Tests.** A fix comes with the test that failed without it. A feature comes
  with tests for what it does and for the ways it refuses.
- **No dependencies.** Collage uses the standard library and nothing else, and
  that is a feature. A change that needs a module is a conversation first.
- **Comments that say why.** The code is commented as prose explaining the
  decision and what goes wrong without it; match that. What the code does is for
  the code to say.
- **Docs.** A change to behaviour updates the matching page under `docs/`, and
  the README if it is something a new user meets.
- **A CHANGELOG entry** under the next version. A change that breaks
  `pkg/collage` or `pkg/collagetest` is not accepted within v1: the `api` CI job
  refuses it, and the only way to make one is a v2 module path. A behaviour
  change that applications must notice still goes first in the entry, under a
  clearly named heading, with what to do instead.

## Checks

CI runs these; run them first:

```
gofmt -l .                  # prints nothing
go vet ./...
go test -race ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
go test ./.github/apidiffgate   # the API gate's own tests
bash .github/apidiff.sh         # the API against the latest release; needs RUNNER_TEMP
```

## Commits

One logical change per commit, with a message in the style of the history:
`fix(csrf): the forgery check reads multipart bodies`, and a body saying what was
wrong and why this is the fix.

Security issues are not contributions in the open — see [SECURITY.md](SECURITY.md).

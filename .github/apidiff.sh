#!/usr/bin/env bash
# Compares the public API with a released tag and writes the report to the job
# summary. Before any v1 tag it only reports; after one it fails on an
# incompatible change against the latest earlier final v1 release.
#
# "The public API" is pkg/collage and pkg/collagetest and also what they alias
# from internal packages, which apidiff does not look inside; .github/apidiffgate
# does that part (see its comment), driving the pinned apidiff below.
set -euo pipefail

# Pinned, so a new apidiff cannot turn a commit red without anything here
# changing: the newest commit of golang.org/x/exp that was at least two weeks
# old when this was written (2026-09-08).
APIDIFF_VERSION=v0.0.0-20260908205506-85c1c2202aba

: "${RUNNER_TEMP:?RUNNER_TEMP must be set (a scratch directory)}"

# Final releases only (a pre-release such as v1.0.0-rc.1 sorts above v1.0.0 and
# must neither be the base nor switch the gate on), and only tags that do not
# contain HEAD: on the push of a tag, the tag itself is HEAD, and comparing a
# release with itself would gate nothing.
final='^v[0-9]+\.[0-9]+\.[0-9]+$'
latest() { git tag --list "$1" --no-contains HEAD --sort=-v:refname | grep -E "$final" | head -n 1 || true; }
v1=$(latest 'v1.*')
base=${v1:-$(latest 'v*')}
if [ -z "$base" ]; then
  echo "no earlier release tag to compare with" >> "${GITHUB_STEP_SUMMARY:-/dev/stdout}"
  exit 0
fi
summary=${GITHUB_STEP_SUMMARY:-/dev/stdout}
echo "## API against ${base}" >> "$summary"

# One build of each tool, so they are not recompiled for every call.
GOBIN="$RUNNER_TEMP" go install "golang.org/x/exp/cmd/apidiff@$APIDIFF_VERSION"
go build -o "$RUNNER_TEMP/apidiffgate" ./.github/apidiffgate

worktree="$RUNNER_TEMP/base"
git worktree add -q --detach "$worktree" "$base"
trap 'git worktree remove --force "$worktree" >/dev/null 2>&1 || true' EXIT

# The report goes to the summary, the incompatible changes to the log. Status 1
# is an incompatible change; 2 is that the comparison itself failed, which after
# a v1 tag fails the job as well: a gate that cannot run has not passed.
status=0
set +e
"$RUNNER_TEMP/apidiffgate" -apidiff "$RUNNER_TEMP/apidiff" -old "$worktree" -new . -tmp "$RUNNER_TEMP" >> "$summary"
gate=$?
set -e
case "$gate" in
  0) ;;
  1) echo "incompatible change against ${base} (listed above)"; [ -n "$v1" ] && status=1 ;;
  *) echo "the API comparison could not run"; [ -n "$v1" ] && status=1 ;;
esac
exit $status

#!/usr/bin/env bash
# Compares pkg/collage and pkg/collagetest with a released tag and writes the
# report to the job summary. Before any v1 tag it only reports; after one it
# fails on an incompatible change against the latest v1 tag.
set -euo pipefail

# Pinned, so a new apidiff cannot turn a commit red without anything here
# changing: the newest commit of golang.org/x/exp that was at least two weeks
# old when this was written (2026-09-08).
APIDIFF_VERSION=v0.0.0-20260908205506-85c1c2202aba

: "${RUNNER_TEMP:?RUNNER_TEMP must be set (a scratch directory)}"
module=$(go list -m)
v1=$(git tag --list 'v1.*' --sort=-v:refname | head -n 1)
base=${v1:-$(git tag --list 'v*' --sort=-v:refname | head -n 1)}
summary=${GITHUB_STEP_SUMMARY:-/dev/stdout}
echo "## API against ${base}" >> "$summary"

# One build of the tool, so it is not recompiled for every call.
GOBIN="$RUNNER_TEMP" go install "golang.org/x/exp/cmd/apidiff@$APIDIFF_VERSION"
apidiff="$RUNNER_TEMP/apidiff"

worktree="$RUNNER_TEMP/base"
git worktree add -q --detach "$worktree" "$base"
trap 'git worktree remove --force "$worktree" >/dev/null 2>&1 || true' EXIT

status=0
for pkg in pkg/collage pkg/collagetest; do
  tag=${pkg//\//_}
  if [ ! -d "$worktree/$pkg" ]; then
    { echo "### $pkg"; echo "not in ${base}"; } >> "$summary"
    continue
  fi
  # -w reads the module of the directory it runs in, so each side is exported
  # from its own checkout.
  (cd "$worktree" && "$apidiff" -w "$RUNNER_TEMP/$tag.old" "$module/$pkg")
  "$apidiff" -w "$RUNNER_TEMP/$tag.new" "$module/$pkg"
  full=$("$apidiff" "$RUNNER_TEMP/$tag.old" "$RUNNER_TEMP/$tag.new")
  report=$("$apidiff" -incompatible "$RUNNER_TEMP/$tag.old" "$RUNNER_TEMP/$tag.new")
  { echo "### $pkg"; echo '```'; echo "${full:-no changes}"; echo '```'; } >> "$summary"
  if [ -n "$v1" ] && [ -n "$report" ]; then
    echo "incompatible change in $pkg against $v1:"
    echo "$report"
    status=1
  fi
done
exit $status

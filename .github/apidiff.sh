#!/usr/bin/env bash
# Compares pkg/collage and pkg/collagetest with a released tag and writes the
# report to the job summary. Before any v1 tag it only reports; after one it
# fails on an incompatible change against the latest earlier final v1 release.
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
  # The module path is read per checkout: a v2 module path is a different
  # import path in the new tree than in the old.
  (cd "$worktree" && "$apidiff" -w "$RUNNER_TEMP/$tag.old" "$(go list -m)/$pkg")
  "$apidiff" -w "$RUNNER_TEMP/$tag.new" "$(go list -m)/$pkg"
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

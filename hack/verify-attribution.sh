#!/bin/sh
# Fails if AI attribution appears in commit messages or tracked files.
#
# Authorship of this repository is a hard requirement, not a preference, and
# tooling defaults actively try to add trailers. Vigilance is not a control:
# this runs in CI so a slip is caught before it becomes permanent history.
#
# Like every guard here, it must fail closed. An unresolvable revision range or
# an empty file list means the check did not run, which is not the same as
# passing.
set -u

# Bracketed first letters so this script does not match itself. Excluding it by
# path instead would leave one file where attribution could hide unchecked.
PATTERN='[c]laude\|[a]nthropic\|[C]o-Authored-By\|[C]o-authored-by\|[G]enerated with'
BASE=${1:-}

status=0

files=$(git ls-files)
if [ -z "$files" ]; then
  echo "ERROR: git ls-files returned nothing; the check did not run"
  exit 1
fi
if hits=$(git grep -In -i "$PATTERN" -- $files 2>/dev/null); then
  echo "ERROR: attribution found in tracked files:"
  echo "$hits"
  status=1
fi

# Commit messages, when a base revision is given or discoverable.
if [ -z "$BASE" ]; then
  BASE=$(git rev-parse --verify -q origin/main 2>/dev/null || echo "")
fi
if [ -n "$BASE" ]; then
  if ! git rev-parse --verify -q "$BASE" >/dev/null; then
    echo "ERROR: base revision '$BASE' does not resolve; cannot check commit messages"
    exit 1
  fi
  msgs=$(git log "$BASE"..HEAD --format='%H%n%B')
  if echo "$msgs" | grep -i "$PATTERN" >/dev/null 2>&1; then
    echo "ERROR: attribution found in commit messages between $BASE and HEAD:"
    git log "$BASE"..HEAD --format='%h %s' | while read -r line; do
      sha=$(echo "$line" | cut -d' ' -f1)
      if git log -1 "$sha" --format='%B' | grep -i "$PATTERN" >/dev/null 2>&1; then
        echo "  $line"
      fi
    done
    status=1
  fi
fi

[ "$status" -eq 0 ] && echo "attribution: clean"
exit "$status"

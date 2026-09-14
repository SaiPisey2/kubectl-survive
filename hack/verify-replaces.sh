#!/bin/sh
# Fails if any k8s.io staging module is required without a matching pin, or
# pinned to the wrong release version.
#
# This is a guard, so every way it could report "ok" without having actually
# checked anything is treated as a failure:
#   - `go list -m all` itself can fail as a whole (a dropped replace resolves
#     to a nonexistent v0.0.0, which is a hard error, not a bad list entry) --
#     that must fail loudly, with the real error shown, not be swallowed.
#   - the k8s.io/ module list coming back empty is not a clean bill of health,
#     it means the query never ran against a real module graph.
#   - k8s.io/kubernetes itself must resolve, since the expected staging
#     version is derived from it.
#   - a staging module pinned to the WRONG release version must fail. Asking
#     "does this module's resolved version equal $SVER" to decide whether the
#     rule even applies to it is circular: a correctly pinned module and one
#     pinned to the wrong release both simply fail to equal $SVER, which is
#     indistinguishable from a module that is exempt on purpose (klog/v2,
#     utils, kube-openapi, gengo/v2, system-validators version independently
#     of the release). The authority on which modules MUST equal $SVER is the
#     `replace` block in go.mod itself: every k8s.io/ line there is, by
#     construction, a staging module this project deliberately pinned.
set -u

if ! command -v jq >/dev/null 2>&1; then
  echo "ERROR: jq is required to read the replace block from go.mod"
  exit 1
fi

KVER=$(go list -m -f '{{.Version}}' k8s.io/kubernetes 2>&1)
kver_status=$?
if [ "$kver_status" -ne 0 ] || [ -z "$KVER" ]; then
  echo "ERROR: could not resolve k8s.io/kubernetes; cannot derive the expected staging version:"
  echo "$KVER"
  exit 1
fi
SVER=$(echo "$KVER" | sed 's|^v1\.|v0.|')

# --- Assertion 1: the module graph resolves and actually contains staging
# modules to check (round 1's fixes: a broken graph or an empty result must
# fail, not be treated as "nothing to complain about"). ---

all_output=$(go list -m -f '{{.Path}}' all 2>&1)
all_status=$?
if [ "$all_status" -ne 0 ]; then
  echo "ERROR: go list -m all failed; the module graph does not resolve:"
  echo "$all_output"
  exit 1
fi

mods=$(printf '%s\n' "$all_output" | grep '^k8s\.io/' | grep -v '^k8s\.io/kubernetes$' || true)
if [ -z "$mods" ]; then
  echo "ERROR: no k8s.io/ staging modules found in the module graph; the query found nothing to check, not a clean result"
  exit 1
fi

# --- Assertion 2: every module go.mod's replace block deliberately pins to
# the kubernetes release must actually be pinned to $SVER, read from an
# authority independent of the module's current resolved version. ---

pins_json=$(go mod edit -json 2>&1)
pins_status=$?
if [ "$pins_status" -ne 0 ]; then
  echo "ERROR: go mod edit -json failed; could not read the replace block:"
  echo "$pins_json"
  exit 1
fi

pins=$(printf '%s\n' "$pins_json" | jq -r '.Replace // [] | .[] | select(.Old.Path | startswith("k8s.io/")) | "\(.Old.Path) \(.New.Version // "MISSING")"')
if [ -z "$pins" ]; then
  echo "ERROR: no k8s.io/ pins found in go.mod's replace block; the guard has nothing to enforce"
  exit 1
fi

status=0

old_ifs=$IFS
IFS='
'
for line in $pins; do
  IFS=' '
  set -- $line
  IFS="$old_ifs"
  mod=$1
  ver=$2
  if [ "$ver" != "$SVER" ]; then
    echo "ERROR: $mod is pinned to '$ver' in go.mod's replace block; expected $SVER (run hack/gen-replaces.sh $KVER)"
    status=1
  fi
  IFS='
'
done
IFS=$old_ifs

[ "$status" -eq 0 ] && echo "staging pins: ok"
exit "$status"

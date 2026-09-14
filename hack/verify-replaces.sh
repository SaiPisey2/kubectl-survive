#!/bin/sh
# Fails if any k8s.io staging module is required without a matching pin.
#
# An unpinned staging module resolves to v0.0.0 and the build breaks in a way
# that reads as a compiler error rather than a dependency error, so this runs in
# CI where the message can say what actually happened.
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
set -u

KVER=$(go list -m -f '{{.Version}}' k8s.io/kubernetes 2>&1)
kver_status=$?
if [ "$kver_status" -ne 0 ] || [ -z "$KVER" ]; then
  echo "ERROR: could not resolve k8s.io/kubernetes; cannot derive the expected staging version:"
  echo "$KVER"
  exit 1
fi
SVER=$(echo "$KVER" | sed 's|^v1\.|v0.|')

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

status=0
for mod in $mods; do
  have=$(go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' "$mod" 2>&1)
  mod_status=$?
  if [ "$mod_status" -ne 0 ]; then
    echo "ERROR: could not resolve $mod:"
    echo "$have"
    status=1
    continue
  fi
  case "$have" in
    "$SVER") ;;
    v0.0.0|"")
      echo "ERROR: $mod resolves to '$have'; pin it to $SVER (run hack/gen-replaces.sh $KVER)"
      status=1
      ;;
    *)
      # k8s.io/klog, k8s.io/utils and friends version independently of a release.
      ;;
  esac
done
[ "$status" -eq 0 ] && echo "staging pins: ok"
exit "$status"

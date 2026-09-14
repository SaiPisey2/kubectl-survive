#!/bin/sh
# Fails if any k8s.io staging module is required without a matching pin.
#
# An unpinned staging module resolves to v0.0.0 and the build breaks in a way
# that reads as a compiler error rather than a dependency error, so this runs in
# CI where the message can say what actually happened.
set -eu
KVER=$(go list -m -f '{{.Version}}' k8s.io/kubernetes)
SVER=$(echo "$KVER" | sed 's|^v1\.|v0.|')

status=0
for mod in $(go list -m -f '{{.Path}}' all 2>/dev/null | grep '^k8s\.io/' | grep -v '^k8s\.io/kubernetes$'); do
  have=$(go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' "$mod" 2>/dev/null || echo "")
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

#!/bin/sh
# Renders the replace block for k8s.io/kubernetes' staging modules.
#
# k8s.io/kubernetes publishes every staging module as v0.0.0 and its own replace
# directives are ignored downstream, so each one needs an explicit pin. Upstream
# is the only authority on which modules exist in a given release, so the list is
# generated from upstream's own go.mod rather than maintained by hand.
#
# Usage: hack/gen-replaces.sh v1.35.7
set -eu
KVER=${1:?usage: gen-replaces.sh vX.Y.Z}
SVER=$(echo "$KVER" | sed 's|^v1\.|v0.|')
curl -sSL "https://raw.githubusercontent.com/kubernetes/kubernetes/${KVER}/go.mod" \
  | sed -n 's|^[[:space:]]*\(k8s\.io/[a-z0-9-]*\) => \./staging/src/k8s\.io/.*|\1 => \1 '"$SVER"'|p'

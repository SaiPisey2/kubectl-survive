#!/usr/bin/env bash
# Builds the demo cluster. Run before recording; everything the GIF shows is
# real output from this cluster, read by the real plugin.
#
# The ordering below is the point, not a convenience. checkout-api is deployed
# while us-east-1a is the only zone that exists, so the scheduler puts all three
# replicas there -- correctly, given what it could see. The other two zones are
# added afterwards. Nothing rebalances, because topology constraints bind at
# scheduling time and there are none here anyway. That is how real clusters end
# up single-zone without anyone making a mistake, and it is exactly what the
# tool exists to surface.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
CLUSTER=${CLUSTER:-survive-demo}

# Pin the control plane to the minor this build of the plugin targets. KWOK
# 0.8.0 defaults to 1.36.1, and the plugin correctly disables fix verification
# on any other minor -- so without this the demo would record the version
# warning instead of the feature. There is no --kube-version flag; the env var
# is the mechanism.
export KWOK_KUBE_VERSION=${KWOK_KUBE_VERSION:-v1.35.7}

node() { sed -e "s/NODENAME/$1/" -e "s/ZONE/$2/" "$HERE/node.tmpl" | kubectl apply -f - >/dev/null; }

kwokctl delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
kwokctl create cluster --name "$CLUSTER" >/dev/null
kubectl config use-context "kwok-$CLUSTER" >/dev/null

# Stage 1: only us-east-1a exists.
for i in 1 2; do node "node-1a-$i" us-east-1a; done
kubectl wait --for=condition=Ready node --all --timeout=60s >/dev/null

# checkout-api and session-store are scheduled now, into the only zone there is.
kubectl apply -f "$HERE/fixtures-stage1.yaml" >/dev/null
kubectl wait --for=condition=Available deploy/checkout-api deploy/session-store --timeout=90s >/dev/null 2>&1 || true

# Stage 2: the cluster grows to three zones. Nothing moves the existing pods.
for i in 1 2; do node "node-1b-$i" us-east-1b; done
for i in 1 2; do node "node-1c-$i" us-east-1c; done
kubectl wait --for=condition=Ready node --all --timeout=60s >/dev/null

# web is deployed only now, after the growth. Deployed earlier, a maxSkew=1
# constraint over a single domain would be trivially satisfied and it would have
# piled into us-east-1a as well -- which is itself worth knowing.
kubectl apply -f "$HERE/fixtures-stage2.yaml" >/dev/null
kubectl rollout status deploy/web --timeout=90s >/dev/null 2>&1 || true

echo "cluster ready:"
kubectl get pods -o custom-columns='POD:.metadata.name,NODE:.spec.nodeName' --no-headers |
  while read -r pod nodename; do
    zone=$(kubectl get node "$nodename" -o jsonpath='{.metadata.labels.topology\.kubernetes\.io/zone}' 2>/dev/null)
    printf '  %-28s %s\n' "$pod" "$zone"
  done

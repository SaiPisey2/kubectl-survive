# Demo

`survive.gif` is generated, not recorded by hand. Everything in it is real
output from a real `kube-apiserver` and `kube-scheduler` running under KWOK.

```sh
./setup.sh                       # build the three-zone cluster
PATH=$PWD/bin:$PATH vhs survive.tape
```

Requires `vhs`, `ttyd`, `ffmpeg`, `kwokctl` and a running Docker, plus a
`kubectl-survive_zone` binary on `PATH`.

## Why the cluster is built in two stages

`setup.sh` creates `us-east-1a`, deploys `checkout-api` into it, and only then
adds `us-east-1b` and `us-east-1c`. The scheduler puts all three replicas in the
one zone that existed at the time, which is correct given what it could see, and
nothing moves them afterwards. That is how real clusters become single-zone
without anyone making a mistake, and it is the situation the tool exists to
surface.

`web` is deployed after the growth for the same reason in reverse: an enforced
`maxSkew: 1` over a single domain is trivially satisfied, so deploying it early
would have piled it into `us-east-1a` too.

The control plane is pinned with `KWOK_KUBE_VERSION` to the Kubernetes minor
this build targets. Without the pin the fix proofs disable themselves with a
version warning, and the demo would record the warning instead of the feature.

## The dependency

`web` is deployed with `SESSIONS=redis://session-store:6379/0` as a literal
environment value, and `session-store` has a real Service. The tool follows the
Service to its EndpointSlice, finds the only backing pod in `us-east-1a`, and
reports `web` as impaired there even though `web` itself is spread one replica
per zone. `web` also points at `orders-db`, an ExternalName Service for a
managed database; that is listed as a dependency and never reported as
impairing, because its placement cannot be proven from the cluster.

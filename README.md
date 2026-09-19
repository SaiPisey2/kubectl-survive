# kubectl survive

Find out whether your cluster actually survives losing a zone.

Most teams believe they are multi-AZ because the manifests say so. The manifests
describe intent; the cluster holds the truth. A `maxSkew: 3` constraint on three
replicas permits all three in one zone. `whenUnsatisfiable: ScheduleAnyway`
quietly gives up under pressure. A node drains at 2am and every replica lands in
the same place. Nothing alerts, and git never changed.

`kubectl survive` reads the real pod placement, removes a failure domain, and
tells you what actually stops serving.

![kubectl survive finding two single-zone workloads on a cluster that reports every deployment healthy, then proving a fix](demo/survive.gif)

Every frame above is real output from a real `kube-apiserver` and
`kube-scheduler`. The cluster reports `3/3` ready for everything; two workloads
would still go dark with one zone. See [demo/](demo/) to reproduce it.

## Install

Download a binary for your platform from the
[releases page](https://github.com/SaiPisey2/kubectl-survive/releases), or:

```sh
go install github.com/SaiPisey2/kubectl-survive/cmd/kubectl-survive@latest
```

Or build from source:

```sh
git clone https://github.com/SaiPisey2/kubectl-survive
cd kubectl-survive && make build
```

Put `kubectl-survive` on your `PATH` and kubectl picks it up as `kubectl survive`.
Check what you have with `kubectl survive version`.

## Use

```
$ kubectl survive

Domain key: topology.kubernetes.io/zone   Snapshot 2026-09-13T20:45:22Z

Losing us-east-1a  ->  2 lost, 0 degraded
  WORKLOAD       PLACEMENT     VERDICT
  checkout-api   us-east-1a:3  LOST all 3 available replicas are in us-east-1a
  session-store  us-east-1a:1  LOST all 1 available replicas are in us-east-1a

Losing us-east-1b  ->  0 lost, 0 degraded
Losing us-east-1c  ->  0 lost, 0 degraded

PDB default/session-store: minAvailable resolves to 1 of 1 pods; no pod can ever be evicted
```

Workloads that survive are omitted. The last line is a separate problem worth
knowing about: that budget can never be satisfied, so any node drain touching
that pod hangs forever.

```sh
kubectl survive                      # every zone
kubectl survive --domain-key rack    # any node label
kubectl survive -o json              # machine readable
```

For every workload reported lost or degraded, `kubectl survive fix` proposes a
ranked ladder of remediations and proves each one against your actual cluster
before printing it:

```sh
kubectl survive fix                        # print verified fixes for every failing workload
kubectl survive fix checkout-api           # only this workload
kubectl survive fix --out-dir ./patches    # write one patch file per fix instead
```

Every fix printed has cleared two proofs, not one:

- **Schedulable** — the real scheduler's Filter plugins, not Score, accept the
  mutated pod on the cluster's actual nodes. A placement that only looks good
  because scoring happened to favour an empty zone is not good enough; Filter
  is what the scheduler must honour.
- **Survives** — survivability analysis, re-run against that placement, no
  longer reports the workload lost.

A fix that clears schedulability but not survivability is still shown,
labelled `ALT` rather than `FIX`, because it helps without solving the
problem. `kubectl survive fix` is read-only: it prints patches, or writes them
to `--out-dir`, and never touches the cluster itself.

## What it checks

- **Real placement**, not declared intent. Constraints bind at scheduling time
  only, so where pods actually are is the only thing that matters.
- **Topology spread** classified as enforced, enforced-but-weak, advisory, or
  absent. A satisfied constraint is not always a useful one.
- **Pod anti-affinity**, required versus preferred.
- **Zonal volumes.** A PersistentVolume pinned to one zone makes that replica
  immovable, whatever the spread constraints say.
- **PodDisruptionBudgets that can never be satisfied**, which do not cause
  outages but do hang node drains indefinitely.

It is read-only. It never evicts, cordons, or deletes anything.

## Correctness

Verdicts are checked against a real Kubernetes control plane, not against
assumptions. The test harness runs an unmodified kube-apiserver, scheduler and
controller-manager under KWOK, predicts the outcome, then actually cordons and
drains a zone through the eviction API and compares.

```sh
go test -tags harness ./test/harness/    # requires Docker and kwokctl
```

## Limits

- Verdicts model an **involuntary** domain loss. When a zone fails its nodes are
  gone and a PodDisruptionBudget offers no protection, so a workload can be
  reported lost here while surviving a `kubectl drain` indefinitely. Both facts
  are true and are reported separately.
- Dependencies between workloads are not yet modelled. A service spread across
  three zones is still reported as surviving even if the database it calls has
  its only replica in the zone that vanished.
- The ladder emits independent, individually actionable rungs (spec §6.1), so
  a workload whose only real remedy is a combination — for example a single
  replica in a single zone, which needs both more replicas and an enforced
  spread — will see each rung reported as partial rather than one combined
  fix.
- Cluster-external dependencies are invisible.

## Requires

Go 1.26 to build. Read access to nodes, pods, PVs, PVCs, PDBs and the apps
workloads. No writes, no in-cluster component.

## Related

`kube-score`, `Polaris` and `Kubescape` check whether you declared redundancy.
This checks whether you have it. They compose well.

## License

Apache-2.0

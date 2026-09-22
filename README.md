# kubectl survive-zone

Find out whether your cluster actually survives losing a zone.

Most teams believe they are multi-AZ because the manifests say so. The manifests
describe intent; the cluster holds the truth. A `maxSkew: 3` constraint on three
replicas permits all three in one zone. `whenUnsatisfiable: ScheduleAnyway`
quietly gives up under pressure. A node drains at 2am and every replica lands in
the same place. Nothing alerts, and git never changed.

`kubectl survive-zone` reads the real pod placement, removes a failure domain, and
tells you what actually stops serving.

![kubectl survive-zone finding two single-zone workloads on a cluster that reports every deployment healthy, then proving a fix](demo/survive.gif)

Every frame above is real output from a real `kube-apiserver` and
`kube-scheduler`. The cluster reports `3/3` ready for everything; two workloads
would still go dark with one zone. See [demo/](demo/) to reproduce it.

## Install

Download a binary for your platform from the
[releases page](https://github.com/SaiPisey2/kubectl-survive/releases), or:

```sh
go install github.com/SaiPisey2/kubectl-survive/cmd/kubectl-survive_zone@latest
```

Or build from source:

```sh
git clone https://github.com/SaiPisey2/kubectl-survive
cd kubectl-survive && make build
```

Put `kubectl-survive_zone` on your `PATH` and kubectl picks it up as
`kubectl survive-zone`. The underscore is how kubectl resolves a hyphenated
plugin command. Check what you have with `kubectl survive-zone version`.

## Use

```
$ kubectl survive-zone

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
kubectl survive-zone                 # every zone
kubectl survive-zone --domain-key rack  # any node label
kubectl survive-zone -o json         # machine readable
```

For every workload reported lost or degraded, `kubectl survive-zone fix` proposes a
ranked ladder of remediations and proves each one against your actual cluster
before printing it:

```sh
kubectl survive-zone fix                        # print verified fixes for every failing workload
kubectl survive-zone fix checkout-api           # only this workload
kubectl survive-zone fix --out-dir ./patches    # write one patch file per fix instead
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
problem. `kubectl survive-zone fix` is read-only: it prints patches, or writes them
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
- **Drain deadlocks that a satisfiable budget alone can't reveal.** A budget
  can look fine in isolation and still hang a drain forever: evict the first
  pod, and if an enforced spread constraint leaves the replacement with
  nowhere to schedule outside the domain being drained, availability never
  recovers and the budget refuses every later eviction. Seeing this needs the
  real scheduler, not arithmetic, which is why it is reported alongside the
  PDB findings above rather than folded into them.
- **Dependency-driven impairment.** Survivability is a graph property, not
  just a per-workload one: a workload whose own pods survive a domain's loss
  can still be **impaired by a dependency** if something it depends on --
  transitively, through Services -- is lost or unknown there. This is
  reported as a separate layer from a workload's own outcome, never folded
  into it: a workload's `survives`/`degraded`/`lost`/`unknown` verdict is
  always defined purely by its own pods. Edges are built only from evidence
  that is provably real:
  - **Service -> backing workload**, from `EndpointSlice` endpoints whose
    `targetRef` is a Pod. EndpointSlices are the ground truth for who a
    Service actually serves; a Service's selector is only intent. A Service
    with no resolvable endpoints is reported **unresolved** -- which counts
    as impairing, the same as "unknown" -- rather than asserted through or
    silently dropped as "no dependency".
  - **Workload -> Service**, from a container's literal env var `value`
    naming the Service by DNS: `svc`, `svc.ns`, `svc.ns.svc`, or
    `svc.ns.svc.<cluster-domain>`. The bare short form `svc` is only accepted
    when the workload is in that Service's own namespace *and* the name
    appears in host position (a URL host, or `host:port`) -- never as a bare
    word alone, since a bare word gives no proof it names anything.
  - A dependency is only asserted when it can be proven; a missing edge is
    preferable to a false one.

It is read-only. It never evicts, cordons, or deletes anything.

## Exporter mode

Survivability **decays**: nothing changes in git, a node drains, and a
workload that used to spread across zones no longer does. A command someone
has to remember to run cannot catch that; a scrape can.

```sh
kubectl survive-zone export --listen :9090 --domain-key topology.kubernetes.io/zone --interval 60s
```

This re-runs the same read-only analysis as the default command on a timer
and serves it at `/metrics`. Analysis runs on its own background loop, never
on the HTTP handler's goroutine, so a slow analysis pass delays the next
reading, not a concurrent scrape.

`--interval` defaults to 60s. A full snapshot fetch plus analysis is not
free — the drain-verification harness measured single scenarios at roughly
6.6s on Apple Silicon and 25s on a GitHub Actions runner, though those are
drain scenarios (which simulate repeated evictions), not the exporter's
single analysis pass. 60s stays comfortably clear of that ceiling while still
catching a drain-induced regression within a minute of it happening.
`--analysis-timeout` (default 2m) bounds a single cycle so a hung API call
cannot silently stop the exporter from ever completing another one.

### Metrics

| Metric | Labels | Meaning |
|---|---|---|
| `survive_workloads_lost` | `domain` | Workloads with zero surviving replicas if this domain is lost. |
| `survive_workloads_degraded` | `domain` | Workloads that fall below their PodDisruptionBudget if this domain is lost. |
| `survive_workload_survives` | `workload`, `domain` | 1 if the workload keeps availability after losing this domain, 0 otherwise (lost, degraded, or unknown). |
| `survive_pdb_unsatisfiable` | `pdb` | 1 if this PodDisruptionBudget can never permit an eviction. |
| `survive_drain_deadlock` | `pdb`, `domain` | 1 if draining this domain deadlocks: the budget is satisfiable in isolation, but the scheduler cannot place the replacement outside the domain. |
| `survive_drain_deadlock_check_enabled` | — | 1 if the scheduler-backed check ran this cycle, 0 if the version gate skipped it. A cluster with nothing wrong and a cluster where this check didn't run must never look the same, so this is reported separately from the finding itself. |
| `survive_unlabelled_nodes` | — | Nodes with no value for the domain-key label; workloads on them are `unknown`, never `survives`. |
| `survive_scrape_success` | — | 1 if the last analysis cycle completed, 0 if it errored. |
| `survive_scrape_errors_total` | — | Cumulative failed analysis cycles. |
| `survive_last_success_timestamp_seconds` | — | Unix time of the last successful analysis. Compare against `time()` to detect a stale exporter even while every finding still reports its last good reading. |
| `survive_last_analysis_duration_seconds` | — | Wall-clock time of the last cycle, successful or not. |

**Why `workload` is `<namespace>/<name>`, not the bare name:** a bare name
collides across namespaces, and a separate `namespace` label multiplies
cardinality for no benefit over folding it into one value.

**Why PDB-backed findings are labelled `pdb`, not `workload`:**
`pdbcheck.Finding` and `draincheck.Finding` are keyed by the
PodDisruptionBudget object, not by the workload it protects — a PDB's
selector isn't resolved back to a single owner. In the common case the PDB is
named after its workload, so this still answers what you'd expect to ask.

**Cardinality:** `survive_workload_survives` is one series per
workload-per-domain, reset and fully repopulated every cycle so a deleted
workload's series disappears on the next scrape rather than accumulating.
For 2,000 workloads across 3 zones that is 6,000 series from one metric —
well inside what a single Prometheus scraping one cluster handles, and it is
also the metric the regression alert depends on directly, so it is not
filtered down to "only the unhealthy ones": doing that would make a
newly-lost workload look identical to a series that was simply never
created, and `changes()`/`resets()`-based alerting needs the continuous 0/1
series to tell those apart. What is deliberately *not* a label anywhere:
free-text reasons (`Verdict.Reason`, `Finding.Detail`) — those belong in the
table/JSON output, and putting unbounded operator-written or generated text
into a label value is the actual cardinality risk, not the workload count.

### The regression alert

The alert this exporter exists to make possible — see
[`deploy/prometheus-rules.yaml`](deploy/prometheus-rules.yaml):

```yaml
- alert: SurvivabilityRegressed
  expr: |
    survive_workload_survives == 0
    and survive_workload_survives offset 1h == 1
    and survive_scrape_success == 1
  for: 5m
```

It fires when a workload's survivability flips from 1 to 0 with the exporter
itself healthy — a node drain, cordon, or manual scale, not a deploy a CI
gate would already have caught. The same file also carries
`SurviveExporterStale` (the exporter hasn't completed an analysis recently:
every other alert is meaningless while this one is firing) and alerts for
unsatisfiable PDBs and drain deadlocks.

[`deploy/grafana-panel.json`](deploy/grafana-panel.json) has a dashboard panel
set built against these metrics, and [`deploy/`](deploy/) has the Deployment,
ServiceAccount, ClusterRole, ClusterRoleBinding and Service to run the
exporter in-cluster with read-only RBAC:

```sh
kubectl apply -f deploy/namespace.yaml -f deploy/rbac.yaml \
              -f deploy/service.yaml -f deploy/deployment.yaml
```

The image is `ghcr.io/saipisey2/kubectl-survive`, published for linux/amd64 and
linux/arm64 with every release. It runs as a non-root user on a read-only root
filesystem.

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
- Dependency edges are read-only by construction (spec §11 wins over §5.5
  where they conflict): only a container's literal env var `value` is ever
  read. `valueFrom`, `envFrom`, and the contents of mounted ConfigMaps or
  Secrets are never followed, so a dependency wired only through one of those
  is invisible here -- it is a missing edge, not a false one. Likewise, a
  Service whose selector matches pods but has no live EndpointSlice
  attribution is reported unresolved rather than guessed at. An
  `ExternalName` Service and a selectorless Service (endpoints managed out
  of band, e.g. a manually-maintained Endpoints object) are shown as
  dependencies but never reported as impairing, because their placement
  can't be proven from the cluster -- a false edge is worse than a missing
  one.
- The ladder emits independent, individually actionable rungs (spec §6.1), so
  a workload whose only real remedy is a combination — for example a single
  replica in a single zone, which needs both more replicas and an enforced
  spread — will see each rung reported as partial rather than one combined
  fix.
- Cluster-external dependencies are invisible.
- Drain-deadlock detection needs the real scheduler, so it is disabled on a
  Kubernetes minor mismatch (the same version gate that disables `fix`
  verification). Domain verdicts and PDB findings keep working regardless;
  the command says plainly when the drain-deadlock check itself was skipped.

## Requires

Go 1.26 to build. Read access to nodes, pods, Services, EndpointSlices, PVs,
PVCs, PDBs and the apps workloads. No writes. The CLI needs nothing installed
in the cluster; the exporter is optional.

## Related

`kube-score`, `Polaris` and `Kubescape` check whether you declared redundancy.
This checks whether you have it. They compose well.

## License

Apache-2.0

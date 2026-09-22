package survive

import (
	"fmt"
	"sort"

	"github.com/SaiPisey2/kubectl-survive/internal/depgraph"
	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/health"
	"github.com/SaiPisey2/kubectl-survive/internal/pdbcheck"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/spread"
	"github.com/SaiPisey2/kubectl-survive/internal/volumepin"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
	corev1 "k8s.io/api/core/v1"
)

type agg struct {
	placement map[string]int
	total     int
	pods      []*corev1.Pod
}

// Analyze removes each domain in turn and reports what stops serving.
func Analyze(s *snapshot.Snapshot, domainKey string) *Report {
	groups, unlabelled := domain.Group(s.Nodes, domainKey)
	idx := workload.NewIndex(s)
	graph := depgraph.Build(s, idx)

	nodeDomain := map[string]string{}
	for d, names := range groups {
		for _, n := range names {
			nodeDomain[n] = d
		}
	}

	byWorkload := map[workload.Ref]*agg{}
	for _, pod := range s.Pods {
		if !health.Available(pod) {
			continue
		}
		ref, ok := idx.Owner(pod)
		if !ok {
			ref = workload.Ref{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
		}
		a := byWorkload[ref]
		if a == nil {
			a = &agg{placement: map[string]int{}}
			byWorkload[ref] = a
		}
		d := nodeDomain[pod.Spec.NodeName] // "" when the node has no domain
		a.placement[d]++
		a.total++
		a.pods = append(a.pods, pod)
	}

	var domains []string
	for d := range groups {
		domains = append(domains, d)
	}
	sort.Strings(domains)

	var refs []workload.Ref
	for ref := range byWorkload {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })

	report := &Report{
		TakenAt:         s.TakenAt,
		DomainKey:       domainKey,
		PDBFindings:     pdbcheck.Analyze(s),
		UnlabelledNodes: unlabelled,
	}

	for _, d := range domains {
		res := DomainResult{Domain: d}
		for _, ref := range refs {
			a := byWorkload[ref]
			survivors := a.total - a.placement[d]

			replicas := a.total
			if n, ok := idx.DesiredReplicas(ref); ok {
				replicas = int(n)
			}

			// Copy the placement map per verdict: a.placement is shared across
			// every domain's Verdict for this workload, and callers must not
			// see one domain's mutations (or future ones) leak into another's.
			placement := make(map[string]int, len(a.placement))
			for k, n := range a.placement {
				placement[k] = n
			}

			v := Verdict{
				Workload:     ref,
				Placement:    placement,
				Spread:       spread.Classify(podSpread(a.pods), domainKey, replicas, len(domains)),
				AntiAffinity: spread.ClassifyAntiAffinity(podAffinity(a.pods), domainKey),
				DependsOn:    graph.DependsOn(ref),
			}
			// A StatefulSet binds a different PV per replica through
			// volumeClaimTemplates, so every pod is checked, not just the
			// first: a pin on any replica makes that replica immovable.
			seenPin := map[string]bool{}
			for _, p := range a.pods {
				for _, pin := range volumepin.ForPod(s, p, domainKey) {
					key := pin.PV + "|" + pin.Domain
					if seenPin[key] {
						continue
					}
					seenPin[key] = true
					v.VolumePins = append(v.VolumePins, pin)
				}
			}

			// Replicas whose volume is pinned to this domain cannot move.
			// Pods already placed in d are already excluded from `survivors`
			// by the placement arithmetic above, so only count a pin as an
			// EXTRA casualty when the pod is not already counted there (the
			// pin points at a domain other than where the pod currently
			// runs) - otherwise a pin on a pod that is already accounted for
			// would double-subtract and wrongly condemn replicas that are
			// not pinned to this domain at all.
			pinnedHere := 0
			for _, p := range a.pods {
				if nodeDomain[p.Spec.NodeName] == d {
					continue
				}
				for _, pin := range volumepin.ForPod(s, p, domainKey) {
					if pin.Domain == d {
						pinnedHere++
						break
					}
				}
			}

			switch {
			case a.total == 0:
				v.Outcome = OutcomeUnknown
				v.Reason = "no available pods found"
			case a.placement[""] > 0:
				// A pod on a node we could not place: we cannot know.
				v.Outcome = OutcomeUnknown
				v.Reason = "one or more pods run on nodes with no resolvable domain"
			case survivors == 0:
				v.Outcome = OutcomeLost
				v.Reason = fmt.Sprintf("all %d available replicas are in %s", a.total, d)
			default:
				// Degraded is defined by the workload's own disruption budget:
				// without one, any surviving replica means the workload serves.
				need, ok, err := pdbcheck.DesiredAvailable(s, a.pods[0], replicas)
				switch {
				case err != nil:
					// An unparseable budget's effect on drains is unknown; that
					// must never render as survives.
					v.Outcome = OutcomeUnknown
					v.Reason = err.Error()
				case ok && survivors < need:
					v.Outcome = OutcomeDegraded
					v.Reason = fmt.Sprintf("%d of %d replicas remain, below the budget's minimum of %d", survivors, a.total, need)
				default:
					v.Outcome = OutcomeSurvives
					v.Reason = fmt.Sprintf("%d of %d replicas remain outside %s", survivors, a.total, d)
				}
			}

			// A replica pinned to the removed domain cannot be rescheduled, so
			// it is lost even if the node-level arithmetic suggested otherwise.
			// It does NOT condemn the whole workload: other replicas placed
			// (and not pinned) outside this domain still serve.
			if pinnedHere > 0 {
				remaining := survivors - pinnedHere
				if remaining < 0 {
					remaining = 0
				}
				if remaining == 0 {
					v.Outcome = OutcomeLost
					v.Reason = fmt.Sprintf("every surviving replica is pinned to %s by a zonal volume", d)
				}
			}

			// Count once, after every override has been applied, so a workload
			// can never appear in two totals.
			switch v.Outcome {
			case OutcomeLost:
				res.Lost++
			case OutcomeDegraded:
				res.Degraded++
			}

			res.Verdicts = append(res.Verdicts, v)
		}

		// Dependency impairment is a separate layer from Lost/Degraded
		// (ruling 1): a workload's own pods can survive losing d while it is
		// still impaired because something it depends on, transitively
		// through Services, is lost or unknown in d.
		lostOrUnknown := map[workload.Ref]bool{}
		ownLost := map[workload.Ref]bool{}
		for _, v := range res.Verdicts {
			switch v.Outcome {
			case OutcomeLost, OutcomeUnknown:
				lostOrUnknown[v.Workload] = true
			}
			if v.Outcome == OutcomeLost {
				ownLost[v.Workload] = true
			}
		}
		res.Impairments = depgraph.Impaired(graph, d, lostOrUnknown, ownLost)
		res.Impaired = len(res.Impairments)

		report.Domains = append(report.Domains, res)
	}
	return report
}

func podSpread(pods []*corev1.Pod) []corev1.TopologySpreadConstraint {
	if len(pods) == 0 {
		return nil
	}
	return pods[0].Spec.TopologySpreadConstraints
}

func podAffinity(pods []*corev1.Pod) *corev1.Affinity {
	if len(pods) == 0 {
		return nil
	}
	return pods[0].Spec.Affinity
}

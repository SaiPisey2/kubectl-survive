// Package draincheck detects a drain that can never finish for a reason
// static PDB arithmetic cannot see: a satisfiable PodDisruptionBudget plus an
// enforced topology spread constraint can still deadlock a domain drain
// forever. Evicting the first replica leaves a replacement that has nowhere
// to schedule -- every surviving domain would violate the spread, and the
// draining domain is being cordoned -- so the budget then refuses every
// later eviction indefinitely.
//
// pdbcheck cannot see this because the budget really is satisfiable in
// isolation; the deadlock only exists once scheduling feasibility is taken
// into account, which is why this package holds a *sched.Scheduler and lives
// outside the survivability core (see the Makefile's CORE dependency guard).
package draincheck

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/health"
	"github.com/SaiPisey2/kubectl-survive/internal/pdbcheck"
	"github.com/SaiPisey2/kubectl-survive/internal/sched"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
)

// Finding is one workload whose PodDisruptionBudget is satisfiable in
// isolation but whose replacement pod cannot schedule anywhere outside the
// domain being drained -- the deadlock described in spec §5.6. It is shaped
// like pdbcheck.Finding on purpose: both answer "what will hang a drain",
// and the render layer prints them side by side.
type Finding struct {
	PDB       string
	Namespace string
	Domain    string
	Detail    string
}

// Detect runs the scheduler-backed check for every domain in snap: for each
// workload that has an available pod in that domain and is covered by a
// PodDisruptionBudget pdbcheck already considers satisfiable, it simulates
// evicting one of those pods and asks the real scheduler whether the
// replacement can land anywhere outside the domain. When it cannot, the
// drain deadlocks the moment that eviction happens for real.
func Detect(ctx context.Context, s *sched.Scheduler, snap *snapshot.Snapshot, domainKey string) ([]Finding, error) {
	groups, _ := domain.Group(snap.Nodes, domainKey)
	if len(groups) == 0 {
		return nil, nil
	}

	nodeDomain := map[string]string{}
	for d, names := range groups {
		for _, n := range names {
			nodeDomain[n] = d
		}
	}

	idx := workload.NewIndex(snap)
	blocks := blockByPDB(snap)

	victims := candidatesByDomain(snap, idx, nodeDomain)

	var domains []string
	for d := range groups {
		domains = append(domains, d)
	}
	sort.Strings(domains)

	var out []Finding
	for _, d := range domains {
		refs := sortedRefs(victims[d])
		for _, ref := range refs {
			pods := victims[d][ref]
			pdb, ok := pdbForPod(snap, pods[0])
			if !ok {
				// No budget, no deadlock: an eviction with nothing refusing
				// it is not blocked, whatever happens to the replacement.
				continue
			}
			if blocks[pdb.Namespace+"/"+pdb.Name] != pdbcheck.BlockNone {
				// Already flagged unsatisfiable (or worse) by pdbcheck; this
				// package's whole reason to exist is the gap where that
				// static check says "fine" and is wrong.
				continue
			}

			sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
			victim := pods[0]

			candidate := replacementCandidate(victim)
			fits, err := s.FitsReplacing(ctx, candidate, domainKey, []*corev1.Pod{victim})
			if err != nil {
				return nil, fmt.Errorf("draincheck: %s losing %s: %w", ref, d, err)
			}

			reason, blocked := onlyFitsInside(fits, d)
			if !blocked {
				continue
			}

			out = append(out, Finding{
				PDB:       pdb.Name,
				Namespace: pdb.Namespace,
				Domain:    d,
				Detail: fmt.Sprintf(
					"draining %s deadlocks: %s's replacement cannot schedule outside it (%s); "+
						"the budget then refuses every later eviction", d, ref, reason),
			})
		}
	}
	return out, nil
}

// onlyFitsInside reports whether every node the real scheduler would accept
// this candidate on lies inside excludeDomain -- the domain being drained.
// A real drain cordons every node in that domain, so those fits are not
// actually available to the replacement; if nothing remains, the eviction
// that produced this candidate would deadlock the drain.
func onlyFitsInside(fits []sched.Fit, excludeDomain string) (reason string, blocked bool) {
	blocked = true
	for _, f := range fits {
		if f.Domain == excludeDomain {
			continue
		}
		if f.OK {
			return "", false
		}
		blocked = true
		if reason == "" {
			reason = f.Reason
		}
	}
	if reason == "" {
		reason = "no node outside the domain accepted the replacement"
	}
	return reason, blocked
}

// candidatesByDomain groups every available, assigned pod by the domain it
// currently runs in and the workload it belongs to.
func candidatesByDomain(snap *snapshot.Snapshot, idx *workload.Index, nodeDomain map[string]string) map[string]map[workload.Ref][]*corev1.Pod {
	out := map[string]map[workload.Ref][]*corev1.Pod{}
	for _, p := range snap.Pods {
		if p.Spec.NodeName == "" || !health.Available(p) {
			continue
		}
		d, ok := nodeDomain[p.Spec.NodeName]
		if !ok {
			continue
		}
		ref, ok := idx.Owner(p)
		if !ok {
			ref = workload.Ref{Kind: "Pod", Namespace: p.Namespace, Name: p.Name}
		}
		byRef := out[d]
		if byRef == nil {
			byRef = map[workload.Ref][]*corev1.Pod{}
			out[d] = byRef
		}
		byRef[ref] = append(byRef[ref], p)
	}
	return out
}

func sortedRefs(byRef map[workload.Ref][]*corev1.Pod) []workload.Ref {
	refs := make([]workload.Ref, 0, len(byRef))
	for ref := range byRef {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
	return refs
}

// blockByPDB runs pdbcheck.Analyze once and indexes its findings by
// "namespace/name" so this package can tell a budget pdbcheck already
// flagged apart from one it considers fine.
func blockByPDB(snap *snapshot.Snapshot) map[string]pdbcheck.Block {
	out := map[string]pdbcheck.Block{}
	for _, f := range pdbcheck.Analyze(snap) {
		out[f.Namespace+"/"+f.PDB] = f.Block
	}
	return out
}

// pdbForPod finds the first PodDisruptionBudget in snap whose selector
// matches pod, mirroring the matching pdbcheck.Analyze itself uses.
func pdbForPod(snap *snapshot.Snapshot, pod *corev1.Pod) (*policyv1.PodDisruptionBudget, bool) {
	for _, p := range snap.PDBs {
		if p.Namespace != pod.Namespace || p.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
		if err != nil {
			continue
		}
		if sel.Matches(labels.Set(pod.Labels)) {
			return p, true
		}
	}
	return nil, false
}

// replacementCandidate builds the unassigned pod FitsReplacing evaluates:
// victim's own spec, so it carries the same topology spread constraints,
// affinity, tolerations and resource requests the real replacement would.
//
// It deliberately does not exclude any domain itself -- see FitsReplacing's
// doc comment for why encoding that exclusion as pod-side node affinity
// would silently loosen the topology spread check instead. The exclusion is
// applied afterward, by the caller, to FitsReplacing's per-node results.
func replacementCandidate(victim *corev1.Pod) *corev1.Pod {
	c := victim.DeepCopy()
	c.Spec.NodeName = ""
	c.ResourceVersion = ""
	c.CreationTimestamp = metav1.Time{}
	c.Status = corev1.PodStatus{}
	return c
}

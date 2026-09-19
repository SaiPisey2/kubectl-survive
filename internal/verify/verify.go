// Package verify runs the two proofs a candidate fix must pass before it is
// shown to an operator: the real scheduler's Filter plugins must accept the
// mutated pod, and the survivability engine, re-run on where the replicas
// would actually land, must no longer report the workload lost.
//
// This lives outside internal/fix on purpose. make verify-deps polices
// internal/fix for scheduler purity so the ladder stays testable without a
// cluster; a Verifier holding a *sched.Scheduler cannot live there.
package verify

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/SaiPisey2/kubectl-survive/internal/fix"
	"github.com/SaiPisey2/kubectl-survive/internal/sched"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
)

// Proof is the answer to both questions for one fix. An error means neither
// question was answered: unknown is never reported as safe, so Schedulable
// and Survives are both left false whenever Err is set.
type Proof struct {
	Schedulable       bool
	SchedulableDetail string
	Survives          bool
	SurvivesDetail    string
	Err               error
}

// Verifier runs both proofs against one point-in-time cluster. It holds the
// real scheduler, so it lives here rather than in internal/fix.
type Verifier struct {
	sched     *sched.Scheduler
	snap      *snapshot.Snapshot
	domainKey string
}

// NewVerifier builds a Verifier over an already-constructed Scheduler.
// Building the framework dominates the cost, so callers running many
// verifications reuse one Scheduler for the whole run.
func NewVerifier(s *sched.Scheduler, snap *snapshot.Snapshot, domainKey string) *Verifier {
	return &Verifier{sched: s, snap: snap, domainKey: domainKey}
}

// Verify runs the two proofs of spec §6. Neither is optional and neither is
// inferred from the other: a fix can schedule perfectly and still leave the
// workload in one zone, and that is the case operators most need told.
func (v *Verifier) Verify(ctx context.Context, in fix.Input, f fix.Fix) Proof {
	var p Proof

	if !f.Fixable() {
		// An architectural finding makes no claim, so it proves nothing.
		return p
	}
	if f.Mutate == nil && f.Replicas == 0 && !isPDBRung(f.Rung) {
		// A fix that changes neither the template, the replica count, nor
		// (via a PDB rung) the disruption budget alters nothing about the
		// cluster this proof would build, so it proves nothing either. Let
		// this fail loudly rather than silently reporting an unearned pass.
		p.Err = fmt.Errorf("rung %d changes nothing observable; refusing to verify it", f.Rung)
		return p
	}

	// Proof 1: does it still schedule?
	tmpl := f.Apply(in.Template)
	replicas := in.Replicas
	if f.Replicas > 0 {
		replicas = f.Replicas
	}
	pod := podFromTemplate(tmpl, in.Verdict.Workload.Namespace, in.Verdict.Workload.Name)

	// A fix to this workload replaces its existing pods; it does not add a
	// second copy of them. PlaceReplacing withdraws exactly those pods before
	// simulating, using the same belongsTo test hypotheticalSnapshot uses
	// below, so the two cannot disagree about which pods this workload owns.
	idx := workload.NewIndex(v.snap)
	existing := ownedPods(v.snap.Pods, idx, in.Verdict.Workload)

	placement, err := v.sched.PlaceReplacing(ctx, pod, replicas, v.domainKey, existing)
	if err != nil {
		// Unknown is never reported as safe.
		p.Err = fmt.Errorf("schedulability: %w", err)
		return p
	}
	if placement.Unschedulable > 0 {
		p.SchedulableDetail = fmt.Sprintf("%d of %d replicas have nowhere to run: %s",
			placement.Unschedulable, replicas, placement.Reason)
		return p
	}
	p.Schedulable = true
	p.SchedulableDetail = describeDomains(placement.Domains)

	// Proof 2: does it survive?
	//
	// The survivability engine is the single definition of "survives". A
	// second implementation of that rule here would let the fix path and the
	// report path disagree about the same workload, which is worse than
	// having no fix path at all. So build the world the fix would produce and
	// ask the engine.
	hypothetical, err := v.hypotheticalSnapshot(in, f, tmpl, placement)
	if err != nil {
		p.Err = fmt.Errorf("survivability: %w", err)
		p.Schedulable, p.Survives = false, false
		return p
	}
	report := survive.Analyze(hypothetical, v.domainKey)
	p.Survives, p.SurvivesDetail = stillServes(report, in.Verdict.Workload)
	return p
}

func isPDBRung(r fix.Rung) bool {
	return r == fix.RungPDBAdd || r == fix.RungPDBRepair
}

// podFromTemplate builds the unassigned pod Place simulates from a mutated
// template and the workload's own identity.
func podFromTemplate(tmpl *corev1.PodTemplateSpec, ns, name string) *corev1.Pod {
	if ns == "" {
		ns = "default"
	}
	if name == "" {
		name = "workload"
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			UID:       types.UID(name),
			Labels:    tmpl.ObjectMeta.Labels,
		},
		Spec: *tmpl.Spec.DeepCopy(),
	}
}

// describeDomains renders a placement's domain counts in a stable order, for
// a human-readable schedulability detail.
func describeDomains(domains map[string]int) string {
	if len(domains) == 0 {
		return "no replicas placed"
	}
	keys := make([]string, 0, len(domains))
	for k := range domains {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, domains[k]))
	}
	return strings.Join(parts, " ")
}

// stillServes finds the verdict for ref in every domain result of report and
// reports false if any domain still reports OutcomeLost, naming that domain.
// OutcomeUnknown is also false: unknown is never safe.
func stillServes(report *survive.Report, ref workload.Ref) (bool, string) {
	for _, dr := range report.Domains {
		for _, verdict := range dr.Verdicts {
			if verdict.Workload != ref {
				continue
			}
			switch verdict.Outcome {
			case survive.OutcomeLost:
				return false, fmt.Sprintf("still lost losing %s: %s", dr.Domain, verdict.Reason)
			case survive.OutcomeUnknown:
				return false, fmt.Sprintf("unknown outcome losing %s: %s", dr.Domain, verdict.Reason)
			}
		}
	}
	return true, "no domain still reports this workload lost"
}

// hypotheticalSnapshot builds the cluster the fix would produce: the real
// snapshot, minus the workload's current pods, plus one pod per entry in
// placement.Nodes carrying the mutated template's labels and the placed node
// name, with the workload's PodDisruptionBudget replaced or added for rungs
// 5 and 6. Everything else, including other workloads' pods, stays exactly
// as observed: a fix is evaluated against the real cluster it will land in,
// not a clean one.
func (v *Verifier) hypotheticalSnapshot(in fix.Input, f fix.Fix, tmpl *corev1.PodTemplateSpec, placement sched.Placement) (*snapshot.Snapshot, error) {
	ref := in.Verdict.Workload
	idx := workload.NewIndex(v.snap)

	pods := make([]*corev1.Pod, 0, len(v.snap.Pods)+len(placement.Nodes))
	for _, p := range v.snap.Pods {
		if belongsTo(p, idx, ref) {
			continue
		}
		pods = append(pods, p)
	}
	for i, node := range placement.Nodes {
		pods = append(pods, syntheticPod(tmpl, ref, node, i))
	}

	selector := workloadSelector(in, tmpl)
	pdbs := make([]*policyv1.PodDisruptionBudget, 0, len(v.snap.PDBs)+1)
	for _, pdb := range v.snap.PDBs {
		if pdb.Namespace == ref.Namespace && pdbSelects(pdb, selector) {
			continue
		}
		pdbs = append(pdbs, pdb)
	}
	switch {
	case isPDBRung(f.Rung):
		pdbs = append(pdbs, syntheticPDB(ref, selector))
	case in.PDB != nil:
		// This fix does not touch the budget; keep the workload's real one
		// visible so the degraded/lost accounting still reflects it.
		kept := in.PDB.DeepCopy()
		kept.Namespace = ref.Namespace
		if kept.Name == "" {
			kept.Name = pdbNameFor(ref)
		}
		if kept.Spec.Selector == nil {
			kept.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
		}
		pdbs = append(pdbs, kept)
	}

	out := *v.snap
	out.Pods = pods
	out.PDBs = pdbs
	return &out, nil
}

// ownedPods returns the subset of pods that belongsTo ref, in the same
// membership every proof in this package uses.
func ownedPods(pods []*corev1.Pod, idx *workload.Index, ref workload.Ref) []*corev1.Pod {
	var out []*corev1.Pod
	for _, p := range pods {
		if belongsTo(p, idx, ref) {
			out = append(out, p)
		}
	}
	return out
}

// belongsTo reports whether pod is a member of ref's workload, using the same
// ownership resolution the real analysis uses. A pod with no controller falls
// back to identity with a bare-Pod ref, matching workload.Index.Owner's own
// fallback in the survivability engine.
func belongsTo(pod *corev1.Pod, idx *workload.Index, ref workload.Ref) bool {
	if r, ok := idx.Owner(pod); ok {
		return r == ref
	}
	return ref.Kind == "Pod" && pod.Namespace == ref.Namespace && pod.Name == ref.Name
}

// syntheticPod builds one placed replica of the mutated template, owned by
// ref so the survivability engine groups it back into the same workload.
func syntheticPod(tmpl *corev1.PodTemplateSpec, ref workload.Ref, node string, i int) *corev1.Pod {
	spec := *tmpl.Spec.DeepCopy()
	spec.NodeName = node

	name := fmt.Sprintf("%s-verify-%d", ref.Name, i)
	var owners []metav1.OwnerReference
	if ref.Kind != "" && ref.Kind != "Pod" {
		controller := true
		owners = []metav1.OwnerReference{{
			Kind:       ref.Kind,
			Name:       ref.Name,
			UID:        types.UID(ref.Name),
			Controller: &controller,
		}}
	} else {
		name = ref.Name
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       ref.Namespace,
			UID:             types.UID(fmt.Sprintf("%s-verify-%d", ref.Name, i)),
			Labels:          tmpl.ObjectMeta.Labels,
			OwnerReferences: owners,
		},
		Spec: spec,
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

// workloadSelector is the label set that identifies this workload's pods,
// preferring the Input's own Selector (the identifying labels the ladder
// already refused to leave empty) and falling back to the mutated template's
// own labels.
func workloadSelector(in fix.Input, tmpl *corev1.PodTemplateSpec) map[string]string {
	if len(in.Selector) > 0 {
		return in.Selector
	}
	return tmpl.ObjectMeta.Labels
}

// pdbSelects reports whether pdb's selector matches selector's labels, used
// to drop the workload's current budget from the hypothetical snapshot
// before it is replaced or kept.
func pdbSelects(pdb *policyv1.PodDisruptionBudget, selector map[string]string) bool {
	if pdb.Spec.Selector == nil {
		return false
	}
	sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
	if err != nil {
		return false
	}
	return sel.Matches(labels.Set(selector))
}

// syntheticPDB is the budget rungs 5 and 6 propose: a satisfiable
// maxUnavailable: 1 over the workload's own selector.
func syntheticPDB(ref workload.Ref, selector map[string]string) *policyv1.PodDisruptionBudget {
	mu := intstr.FromInt32(1)
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: pdbNameFor(ref), Namespace: ref.Namespace},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &mu,
			Selector:       &metav1.LabelSelector{MatchLabels: selector},
		},
	}
}

func pdbNameFor(ref workload.Ref) string {
	if ref.Name != "" {
		return ref.Name + "-pdb"
	}
	return "workload-pdb"
}

package verify

import (
	"context"
	"fmt"
	"sort"

	policyv1 "k8s.io/api/policy/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/fix"
	"github.com/SaiPisey2/kubectl-survive/internal/sched"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
)

// Verified is one candidate fix and its two proofs.
type Verified struct {
	Fix     fix.Fix
	Proof   Proof
	Partial bool
}

// Result is one failing workload's ranked fixes, the architectural findings
// that have no patch, and, when a proof could not be completed, the reason
// nothing more is claimed.
type Result struct {
	Workload      workload.Ref
	Domain        string
	Cause         string
	Fixes         []Verified
	Architectural []fix.Fix
}

// Analyze runs the ladder and both proofs for every workload report already
// found losing or degraded, across every domain in s. only, when non-empty,
// restricts the run to workloads whose name appears in it.
//
// One Scheduler and one Verifier are built for the whole run: framework
// construction dominates the cost, and Verifier.Verify restores the cache
// after each simulation, so reusing both across every candidate is correct as
// well as fast.
func Analyze(ctx context.Context, s *snapshot.Snapshot, report *survive.Report, domainKey string, only []string) ([]Result, error) {
	sc, err := sched.New(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("building scheduler: %w", err)
	}
	v := NewVerifier(sc, s, domainKey)

	_, domains := domain.Group(s.Nodes, domainKey)
	sort.Strings(domains)

	wanted := workloadFilter(only)
	pdbs := pdbsByWorkload(s)
	idx := workload.NewIndex(s)

	var results []Result
	for _, dr := range report.Domains {
		for _, verdict := range dr.Verdicts {
			if verdict.Outcome != survive.OutcomeLost && verdict.Outcome != survive.OutcomeDegraded {
				continue
			}
			if !wanted(verdict.Workload) {
				continue
			}

			res := Result{Workload: verdict.Workload, Domain: dr.Domain, Cause: verdict.Reason}

			tmpl, replicas, selector, ok := resolveWorkload(s, idx, verdict.Workload)
			if !ok {
				results = append(results, res)
				continue
			}

			in := fix.Input{
				Verdict:   verdict,
				Template:  tmpl,
				Replicas:  replicas,
				DomainKey: domainKey,
				Domains:   domains,
				Selector:  selector,
				PDB:       pdbs[verdict.Workload],
			}

			for _, f := range fix.Candidates(in) {
				if !f.Fixable() {
					res.Architectural = append(res.Architectural, f)
					continue
				}
				proof := v.Verify(ctx, in, f)
				if proof.Err != nil {
					res.Cause = fmt.Sprintf("%s (rung %d check incomplete: %s)", res.Cause, f.Rung, proof.Err)
					continue
				}
				if !proof.Schedulable {
					continue
				}
				res.Fixes = append(res.Fixes, Verified{Fix: f, Proof: proof, Partial: !proof.Survives})
			}

			sort.SliceStable(res.Fixes, func(i, j int) bool {
				if res.Fixes[i].Partial != res.Fixes[j].Partial {
					return !res.Fixes[i].Partial
				}
				return res.Fixes[i].Fix.Rung < res.Fixes[j].Fix.Rung
			})

			results = append(results, res)
		}
	}
	return results, nil
}

// workloadFilter builds the membership test only encodes: everything matches
// when only is empty, otherwise a workload matches when its name appears in
// only.
func workloadFilter(only []string) func(workload.Ref) bool {
	if len(only) == 0 {
		return func(workload.Ref) bool { return true }
	}
	set := make(map[string]bool, len(only))
	for _, name := range only {
		set[name] = true
	}
	return func(ref workload.Ref) bool { return set[ref.Name] }
}

// pdbsByWorkload maps each workload to the first PodDisruptionBudget in s
// whose selector matches that workload's pods.
func pdbsByWorkload(s *snapshot.Snapshot) map[workload.Ref]*policyv1.PodDisruptionBudget {
	idx := workload.NewIndex(s)
	out := map[workload.Ref]*policyv1.PodDisruptionBudget{}
	for _, pdb := range s.PDBs {
		if pdb.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil {
			continue
		}
		for _, pod := range s.Pods {
			if pod.Namespace != pdb.Namespace || !sel.Matches(labels.Set(pod.Labels)) {
				continue
			}
			ref, ok := idx.Owner(pod)
			if !ok {
				ref = workload.Ref{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
			}
			if _, exists := out[ref]; !exists {
				out[ref] = pdb
			}
		}
	}
	return out
}

// resolveWorkload finds the object behind ref and returns its pod template,
// declared replica count and identifying selector. ok is false when ref
// cannot be resolved to an object this package knows how to read a template
// from, in which case Analyze still reports the failure but offers no fixes.
func resolveWorkload(s *snapshot.Snapshot, idx *workload.Index, ref workload.Ref) (*corev1.PodTemplateSpec, int, map[string]string, bool) {
	switch ref.Kind {
	case "Deployment":
		for _, d := range s.Deployments {
			if d.Namespace == ref.Namespace && d.Name == ref.Name {
				return &d.Spec.Template, replicaCount(d.Spec.Replicas, idx, ref), selectorLabels(d.Spec.Selector, d.Spec.Template.Labels), true
			}
		}
	case "StatefulSet":
		for _, st := range s.StatefulSets {
			if st.Namespace == ref.Namespace && st.Name == ref.Name {
				return &st.Spec.Template, replicaCount(st.Spec.Replicas, idx, ref), selectorLabels(st.Spec.Selector, st.Spec.Template.Labels), true
			}
		}
	}
	return nil, 0, nil, false
}

func replicaCount(specReplicas *int32, idx *workload.Index, ref workload.Ref) int {
	if specReplicas != nil {
		return int(*specReplicas)
	}
	if n, ok := idx.DesiredReplicas(ref); ok {
		return int(n)
	}
	return 0
}

// selectorLabels prefers the object's own match labels and falls back to the
// pod template's own labels when the selector is empty or absent.
func selectorLabels(sel *metav1.LabelSelector, fallback map[string]string) map[string]string {
	if sel != nil && len(sel.MatchLabels) > 0 {
		return sel.MatchLabels
	}
	return fallback
}

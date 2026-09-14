package fix

import (
	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/spread"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// zoneKey is the domain key used by every test in this package that does not
// exercise a different topology key on purpose.
const zoneKey = domain.LabelZone

// templateWithLabels builds a minimal pod template carrying the given pod
// labels and no topology spread constraints. Later tasks add more helpers
// here as the ladder grows more rungs to exercise.
func templateWithLabels(labels map[string]string) *corev1.PodTemplateSpec {
	return &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "example/app:latest"}},
		},
	}
}

// templateWithSpread builds a template carrying a single topology spread
// constraint on zoneKey with the given unsatisfiable policy and maxSkew.
func templateWithSpread(when corev1.UnsatisfiableConstraintAction, maxSkew int32) *corev1.PodTemplateSpec {
	return templateWithSpreadKey(zoneKey, when, maxSkew)
}

// templateWithSpreadKey is templateWithSpread with an explicit topology key,
// for tests proving a rung leaves other topology keys alone.
func templateWithSpreadKey(key string, when corev1.UnsatisfiableConstraintAction, maxSkew int32) *corev1.PodTemplateSpec {
	t := templateWithLabels(map[string]string{"app": "web"})
	t.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
		MaxSkew:           maxSkew,
		TopologyKey:       key,
		WhenUnsatisfiable: when,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
	}}
	return t
}

// verdictWithSpread builds a survive.Verdict carrying only the spread
// assessment a test cares about. Later tasks add more fields as more rungs
// consume more of the verdict.
func verdictWithSpread(state spread.State) survive.Verdict {
	return survive.Verdict{
		Workload: workload.Ref{Kind: "Deployment", Namespace: "default", Name: "web"},
		Outcome:  survive.OutcomeLost,
		Spread:   spread.Assessment{State: state},
	}
}

// inputFor is the common case: a template, a spread state, and a replica
// count, spread across three zones with the workload's own labels as the
// selector. The verdict's AntiAffinity assessment is derived from the
// template itself, the same way the real analysis pipeline computes it, so
// a template built with templateWithPreferredZoneAntiAffinity correctly
// produces an advisory assessment without every caller having to set it by
// hand. Tests that need something more specific build an Input literal.
func inputFor(tmpl *corev1.PodTemplateSpec, state spread.State, replicas int) Input {
	v := verdictWithSpread(state)
	v.AntiAffinity = spread.ClassifyAntiAffinity(tmpl.Spec.Affinity, zoneKey)
	return Input{
		Verdict:   v,
		Template:  tmpl,
		Replicas:  replicas,
		DomainKey: zoneKey,
		Domains:   []string{"zone-a", "zone-b", "zone-c"},
		Selector:  map[string]string{"app": "web"},
	}
}

// templateWithPreferredZoneAntiAffinity builds a template carrying a single
// preferred (advisory) pod anti-affinity term on zoneKey, for rung 7 tests.
func templateWithPreferredZoneAntiAffinity() *corev1.PodTemplateSpec {
	t := templateWithLabels(map[string]string{"app": "web"})
	t.Spec.Affinity = &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
				Weight: 100,
				PodAffinityTerm: corev1.PodAffinityTerm{
					TopologyKey:   zoneKey,
					LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
				},
			}},
		},
	}
	return t
}

// pdbMinAvailable builds a PodDisruptionBudget carrying only a minAvailable
// field, for rung 6 tests. Its selector is left unset: rung 6's synthetic
// satisfiability check aligns the selector with the Input under test rather
// than trusting whatever selector the caller's PDB happens to carry.
func pdbMinAvailable(n int32) *policyv1.PodDisruptionBudget {
	v := intstr.FromInt32(n)
	return &policyv1.PodDisruptionBudget{Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &v}}
}

// pdbMaxUnavailable builds a PodDisruptionBudget carrying only a
// maxUnavailable field, for rung 5 tests.
func pdbMaxUnavailable(n int32) *policyv1.PodDisruptionBudget {
	v := intstr.FromInt32(n)
	return &policyv1.PodDisruptionBudget{Spec: policyv1.PodDisruptionBudgetSpec{MaxUnavailable: &v}}
}

// findRung returns the fix at the given rung, or nil if none was generated.
func findRung(fixes []Fix, r Rung) *Fix {
	for i := range fixes {
		if fixes[i].Rung == r {
			return &fixes[i]
		}
	}
	return nil
}

// mustFindRung is findRung but fails the test when the rung is missing.
func mustFindRung(t interface {
	Helper()
	Fatalf(string, ...any)
}, fixes []Fix, r Rung) Fix {
	t.Helper()
	f := findRung(fixes, r)
	if f == nil {
		t.Fatalf("rung %d was not generated", r)
	}
	return *f
}

// rungsOf is a debugging aid for order-sensitive assertions.
func rungsOf(fixes []Fix) []Rung {
	out := make([]Rung, len(fixes))
	for i, f := range fixes {
		out[i] = f.Rung
	}
	return out
}

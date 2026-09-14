package fix

import (
	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/spread"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
// selector. Tests that need something more specific build an Input literal.
func inputFor(tmpl *corev1.PodTemplateSpec, state spread.State, replicas int) Input {
	return Input{
		Verdict:   verdictWithSpread(state),
		Template:  tmpl,
		Replicas:  replicas,
		DomainKey: zoneKey,
		Domains:   []string{"zone-a", "zone-b", "zone-c"},
		Selector:  map[string]string{"app": "web"},
	}
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

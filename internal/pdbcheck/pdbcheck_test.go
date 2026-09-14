// internal/pdbcheck/pdbcheck_test.go
package pdbcheck

import (
	"testing"

	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func pdb(name string, minAvail, maxUnavail *intstr.IntOrString, sel map[string]string, gen, observed int64) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Generation: gen},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable:   minAvail,
			MaxUnavailable: maxUnavail,
			Selector:       &metav1.LabelSelector{MatchLabels: sel},
		},
		Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: observed},
	}
}

func readyPod(name string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: labels},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

func terminatingPod(name string, labels map[string]string) *corev1.Pod {
	now := metav1.Now()
	p := readyPod(name, labels)
	p.DeletionTimestamp = &now
	return p
}

func findingFor(fs []Finding, name string) *Finding {
	for i := range fs {
		if fs[i].PDB == name {
			return &fs[i]
		}
	}
	return nil
}

func TestNeverSatisfiableCases(t *testing.T) {
	one := intstr.FromInt32(1)
	hundred := intstr.FromString("100%")
	zero := intstr.FromInt32(0)

	// Each PDB must select a DISTINCT pod set. A pod selected by two budgets
	// is BlockMultiplePDBs, which is checked first and would mask the case
	// under test here.
	a := map[string]string{"app": "a"}
	b := map[string]string{"app": "b"}
	c := map[string]string{"app": "c"}

	s := &snapshot.Snapshot{
		Pods: []*corev1.Pod{readyPod("a-1", a), readyPod("b-1", b), readyPod("c-1", c)},
		PDBs: []*policyv1.PodDisruptionBudget{
			pdb("min-equals-replicas", &one, nil, a, 1, 1),
			pdb("min-100-percent", &hundred, nil, b, 1, 1),
			pdb("max-unavailable-zero", nil, &zero, c, 1, 1),
		},
	}
	got := Analyze(s)
	for _, name := range []string{"min-equals-replicas", "min-100-percent", "max-unavailable-zero"} {
		if f := findingFor(got, name); f == nil || f.Block != BlockNeverSatisfiable {
			t.Errorf("%s: got %+v, want BlockNeverSatisfiable", name, f)
		}
	}
}

func TestSatisfiablePDBIsNotFlagged(t *testing.T) {
	one := intstr.FromInt32(1)
	app := map[string]string{"app": "x"}
	s := &snapshot.Snapshot{
		Pods: []*corev1.Pod{readyPod("x-1", app), readyPod("x-2", app), readyPod("x-3", app)},
		PDBs: []*policyv1.PodDisruptionBudget{pdb("healthy", &one, nil, app, 1, 1)},
	}
	if f := findingFor(Analyze(s), "healthy"); f != nil && f.Block != BlockNone {
		t.Errorf("got %+v, want no block", f)
	}
}

// Two PDBs matching one pod means eviction fails with HTTP 500 forever (#75957).
func TestMultiplePDBsMatchingOnePod(t *testing.T) {
	one := intstr.FromInt32(1)
	app := map[string]string{"app": "x"}
	s := &snapshot.Snapshot{
		Pods: []*corev1.Pod{readyPod("x-1", app), readyPod("x-2", app), readyPod("x-3", app)},
		PDBs: []*policyv1.PodDisruptionBudget{pdb("a", &one, nil, app, 1, 1), pdb("b", &one, nil, app, 1, 1)},
	}
	got := Analyze(s)
	for _, name := range []string{"a", "b"} {
		if f := findingFor(got, name); f == nil || f.Block != BlockMultiplePDBs {
			t.Errorf("%s: got %+v, want BlockMultiplePDBs", name, f)
		}
	}
}

func TestSelectorMatchingNoPods(t *testing.T) {
	one := intstr.FromInt32(1)
	s := &snapshot.Snapshot{
		Pods: []*corev1.Pod{readyPod("x-1", map[string]string{"app": "x"})},
		PDBs: []*policyv1.PodDisruptionBudget{pdb("orphan", &one, nil, map[string]string{"app": "nothing"}, 1, 1)},
	}
	if f := findingFor(Analyze(s), "orphan"); f == nil || f.Block != BlockNoPodsMatched {
		t.Errorf("got %+v, want BlockNoPodsMatched", f)
	}
}

func TestStaleStatusIsReportedNotTrusted(t *testing.T) {
	one := intstr.FromInt32(1)
	app := map[string]string{"app": "x"}
	s := &snapshot.Snapshot{
		Pods: []*corev1.Pod{readyPod("x-1", app), readyPod("x-2", app)},
		PDBs: []*policyv1.PodDisruptionBudget{pdb("stale", &one, nil, app, 5, 4)},
	}
	if f := findingFor(Analyze(s), "stale"); f == nil || f.Block != BlockStaleStatus {
		t.Errorf("got %+v, want BlockStaleStatus", f)
	}
}

// Percentages round UP for both fields.
func TestPercentageRoundingIsCeiling(t *testing.T) {
	fifty := intstr.FromString("50%")
	if got := requiredAvailable(&fifty, 3); got != 2 {
		t.Errorf("requiredAvailable(50%%, 3) = %d, want 2", got)
	}
	if got := requiredAvailable(&fifty, 7); got != 4 {
		t.Errorf("requiredAvailable(50%%, 7) = %d, want 4", got)
	}
}

// A terminating pod is never subject to a PDB (the API server's
// canIgnorePDB skips it outright), so it must not inflate the matched-pod
// denominator: a replicas:1, minAvailable:1 Deployment that momentarily has
// a Terminating pod alongside its one Ready replacement is still
// NeverSatisfiable, not "satisfiable" against a transient count of 2.
func TestTerminatingPodDoesNotInflateDenominator(t *testing.T) {
	one := intstr.FromInt32(1)
	app := map[string]string{"app": "x"}
	s := &snapshot.Snapshot{
		Pods: []*corev1.Pod{readyPod("x-new", app), terminatingPod("x-old", app)},
		PDBs: []*policyv1.PodDisruptionBudget{pdb("mid-rollout", &one, nil, app, 1, 1)},
	}
	if f := findingFor(Analyze(s), "mid-rollout"); f == nil || f.Block != BlockNeverSatisfiable {
		t.Errorf("got %+v, want BlockNeverSatisfiable", f)
	}
}

// An empty-but-present selector matches EVERY pod in the namespace in
// policy/v1, so such a budget must be evaluated, not skipped as protecting
// nothing.
func TestEmptySelectorMatchesEveryPodInNamespace(t *testing.T) {
	one := intstr.FromInt32(1)
	s := &snapshot.Snapshot{
		Pods: []*corev1.Pod{readyPod("only-pod", map[string]string{"app": "x"})},
		PDBs: []*policyv1.PodDisruptionBudget{{
			ObjectMeta: metav1.ObjectMeta{Name: "catch-all", Namespace: "default", Generation: 1},
			Spec: policyv1.PodDisruptionBudgetSpec{
				MinAvailable: &one,
				Selector:     &metav1.LabelSelector{},
			},
			Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1},
		}},
	}
	f := findingFor(Analyze(s), "catch-all")
	if f == nil {
		t.Fatal("no finding produced for catch-all")
	}
	if f.Block == BlockNoPodsMatched {
		t.Errorf("empty selector reported as matching no pods; it matches every pod in the namespace")
	}
	// One pod, minAvailable 1 -> no pod can ever be evicted.
	if f.Block != BlockNeverSatisfiable {
		t.Errorf("got %+v, want BlockNeverSatisfiable", f)
	}
}

// A budget with an unparseable selector must surface an error, not silently
// report "no budget selects this pod" (which the caller would render as
// survives — unknown-as-safe is exactly what this tool must never do).
func TestDesiredAvailableReturnsErrorOnBadSelector(t *testing.T) {
	app := map[string]string{"app": "x"}
	one := intstr.FromInt32(1)
	bad := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "default"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &one,
			Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "app", Operator: "NotAnOperator", Values: []string{"x"}},
			}},
		},
	}
	s := &snapshot.Snapshot{
		Pods: []*corev1.Pod{readyPod("x-1", app)},
		PDBs: []*policyv1.PodDisruptionBudget{bad},
	}
	_, _, err := DesiredAvailable(s, s.Pods[0], 1)
	if err == nil {
		t.Fatal("DesiredAvailable() error = nil, want an error for the unparseable selector")
	}
}

func TestDesiredAvailableClampsMaxUnavailableOver100Percent(t *testing.T) {
	app := map[string]string{"app": "x"}
	hundredFifty := intstr.FromString("150%")
	s := &snapshot.Snapshot{
		PDBs: []*policyv1.PodDisruptionBudget{pdb("over", nil, &hundredFifty, app, 1, 1)},
	}
	pod := readyPod("x-1", app)
	need, ok, err := DesiredAvailable(s, pod, 2)
	if err != nil {
		t.Fatalf("DesiredAvailable() error = %v", err)
	}
	if !ok {
		t.Fatal("DesiredAvailable() ok = false, want true")
	}
	if need != 0 {
		t.Errorf("need = %d, want 0 (clamped)", need)
	}
}

package survive

import (
	"testing"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func i32(i int32) *int32 { return &i }
func bp(b bool) *bool    { return &b }

func testNode(name, zone string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: name, Labels: map[string]string{domain.LabelZone: zone},
	}}
}

// deployWith builds a Deployment, its ReplicaSet, and pods placed on nodes.
func deployWith(name string, replicas int32, nodes []string) (*appsv1.Deployment, *appsv1.ReplicaSet, []*corev1.Pod) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name + "-dep")},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(replicas)},
	}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: name + "-rs", Namespace: "default", UID: types.UID(name + "-rs"),
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: name, UID: dep.UID, Controller: bp(true)}},
	}}
	var pods []*corev1.Pod
	for i, n := range nodes {
		pods = append(pods, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name + "-" + string(rune('a'+i)), Namespace: "default",
				Labels:          map[string]string{"app": name},
				OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: bp(true)}},
			},
			Spec: corev1.PodSpec{NodeName: n},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			},
		})
	}
	return dep, rs, pods
}

func verdictFor(r *Report, zone, name string) *Verdict {
	for _, d := range r.Domains {
		if d.Domain != zone {
			continue
		}
		for i := range d.Verdicts {
			if d.Verdicts[i].Workload.Name == name {
				return &d.Verdicts[i]
			}
		}
	}
	return nil
}

func TestAllReplicasInOneZoneIsLost(t *testing.T) {
	dep, rs, pods := deployWith("checkout", 3, []string{"n1a", "n1a", "n1a"})
	s := &snapshot.Snapshot{
		Nodes:       []*corev1.Node{testNode("n1a", "us-east-1a"), testNode("n1b", "us-east-1b")},
		Pods:        pods,
		ReplicaSets: []*appsv1.ReplicaSet{rs},
		Deployments: []*appsv1.Deployment{dep},
	}
	r := Analyze(s, domain.LabelZone)

	if v := verdictFor(r, "us-east-1a", "checkout"); v == nil || v.Outcome != OutcomeLost {
		t.Fatalf("us-east-1a verdict = %+v, want OutcomeLost", v)
	}
	if v := verdictFor(r, "us-east-1b", "checkout"); v == nil || v.Outcome != OutcomeSurvives {
		t.Errorf("us-east-1b verdict = %+v, want OutcomeSurvives", v)
	}
}

func TestSpreadReplicasSurvive(t *testing.T) {
	dep, rs, pods := deployWith("api", 3, []string{"n1a", "n1b", "n1c"})
	s := &snapshot.Snapshot{
		Nodes:       []*corev1.Node{testNode("n1a", "us-east-1a"), testNode("n1b", "us-east-1b"), testNode("n1c", "us-east-1c")},
		Pods:        pods,
		ReplicaSets: []*appsv1.ReplicaSet{rs},
		Deployments: []*appsv1.Deployment{dep},
	}
	r := Analyze(s, domain.LabelZone)
	if v := verdictFor(r, "us-east-1a", "api"); v == nil || v.Outcome != OutcomeSurvives {
		t.Errorf("verdict = %+v, want OutcomeSurvives", v)
	}
}

// Degraded is defined by the PodDisruptionBudget: 1 surviving replica under a
// minAvailable of 2 is degraded, whereas the same loss with no budget is not.
func TestDegradedWhenSurvivorsFallBelowTheBudget(t *testing.T) {
	dep, rs, pods := deployWith("mixed", 3, []string{"n1a", "n1a", "n1b"})
	two := intstr.FromInt32(2)
	s := &snapshot.Snapshot{
		Nodes:       []*corev1.Node{testNode("n1a", "us-east-1a"), testNode("n1b", "us-east-1b")},
		Pods:        pods,
		ReplicaSets: []*appsv1.ReplicaSet{rs},
		Deployments: []*appsv1.Deployment{dep},
		PDBs: []*policyv1.PodDisruptionBudget{{
			ObjectMeta: metav1.ObjectMeta{Name: "mixed", Namespace: "default", Generation: 1},
			Spec: policyv1.PodDisruptionBudgetSpec{
				MinAvailable: &two,
				Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "mixed"}},
			},
			Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1},
		}},
	}
	r := Analyze(s, domain.LabelZone)
	if v := verdictFor(r, "us-east-1a", "mixed"); v == nil || v.Outcome != OutcomeDegraded {
		t.Errorf("verdict = %+v, want OutcomeDegraded", v)
	}
}

// The same partial loss with NO budget is a surviving workload, not a degraded one.
func TestPartialLossWithoutBudgetSurvives(t *testing.T) {
	dep, rs, pods := deployWith("nobudget", 3, []string{"n1a", "n1a", "n1b"})
	s := &snapshot.Snapshot{
		Nodes:       []*corev1.Node{testNode("n1a", "us-east-1a"), testNode("n1b", "us-east-1b")},
		Pods:        pods,
		ReplicaSets: []*appsv1.ReplicaSet{rs},
		Deployments: []*appsv1.Deployment{dep},
	}
	r := Analyze(s, domain.LabelZone)
	if v := verdictFor(r, "us-east-1a", "nobudget"); v == nil || v.Outcome != OutcomeSurvives {
		t.Errorf("verdict = %+v, want OutcomeSurvives", v)
	}
}

// A node we cannot place must never be silently ignored.
func TestUnlabelledNodesAreReported(t *testing.T) {
	s := &snapshot.Snapshot{Nodes: []*corev1.Node{
		testNode("n1a", "us-east-1a"),
		{ObjectMeta: metav1.ObjectMeta{Name: "mystery"}},
	}}
	r := Analyze(s, domain.LabelZone)
	if len(r.UnlabelledNodes) != 1 || r.UnlabelledNodes[0] != "mystery" {
		t.Errorf("UnlabelledNodes = %v, want [mystery]", r.UnlabelledNodes)
	}
}

// Hard rule: a pod on a node whose domain cannot be resolved makes the verdict
// unknown. It must never be reported as surviving.
func TestUnknownIsNeverReportedAsSurvives(t *testing.T) {
	dep, rs, pods := deployWith("mystery", 2, []string{"n1a", "orphan"})
	s := &snapshot.Snapshot{
		Nodes: []*corev1.Node{
			testNode("n1a", "us-east-1a"),
			{ObjectMeta: metav1.ObjectMeta{Name: "orphan"}}, // no zone label
		},
		Pods:        pods,
		ReplicaSets: []*appsv1.ReplicaSet{rs},
		Deployments: []*appsv1.Deployment{dep},
	}
	r := Analyze(s, domain.LabelZone)
	if v := verdictFor(r, "us-east-1a", "mystery"); v == nil || v.Outcome != OutcomeUnknown {
		t.Errorf("verdict = %+v, want OutcomeUnknown", v)
	}
}

// Hard rule: a volume pinned to the removed domain overrides replica
// arithmetic, and the workload is counted exactly once in the domain totals.
func TestPinnedReplicaDoesNotCondemnSurvivingReplicas(t *testing.T) {
	dep, rs, pods := deployWith("db", 2, []string{"n1b", "n1a"})
	// The pod in us-east-1a owns the zonal volume: NOT the first pod.
	pods[1].Spec.Volumes = []corev1.Volume{{
		Name: "data",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-db-1"},
		},
	}}
	s := &snapshot.Snapshot{
		Nodes:       []*corev1.Node{testNode("n1a", "us-east-1a"), testNode("n1b", "us-east-1b")},
		Pods:        pods,
		ReplicaSets: []*appsv1.ReplicaSet{rs},
		Deployments: []*appsv1.Deployment{dep},
		PVCs: []*corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: "data-db-1", Namespace: "default"},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pv-db-1"},
		}},
		PVs: []*corev1.PersistentVolume{{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-db-1"},
			Spec: corev1.PersistentVolumeSpec{
				NodeAffinity: &corev1.VolumeNodeAffinity{
					Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
						MatchExpressions: []corev1.NodeSelectorRequirement{{
							Key:      domain.LabelZone,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"us-east-1a"},
						}},
					}}},
				},
			},
		}},
	}

	r := Analyze(s, domain.LabelZone)
	v := verdictFor(r, "us-east-1a", "db")
	if v == nil || v.Outcome != OutcomeSurvives {
		t.Fatalf("verdict = %+v, want OutcomeSurvives: the unpinned replica in us-east-1b still serves", v)
	}
	if len(v.VolumePins) != 1 || v.VolumePins[0].Domain != "us-east-1a" {
		t.Errorf("VolumePins = %+v, want the pin to us-east-1a still recorded", v.VolumePins)
	}
	for _, dr := range r.Domains {
		if dr.Domain != "us-east-1a" {
			continue
		}
		if dr.Lost != 0 || dr.Degraded != 0 {
			t.Errorf("domain totals = %d lost, %d degraded; want 0 lost and 0 degraded", dr.Lost, dr.Degraded)
		}
	}
}

// When the ONLY replica is pinned to the domain being removed, it has nowhere
// else to run and the workload really is lost.
func TestOnlyReplicaPinnedToRemovedDomainIsLost(t *testing.T) {
	dep, rs, pods := deployWith("db", 1, []string{"n1a"})
	pods[0].Spec.Volumes = []corev1.Volume{{
		Name: "data",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-db-0"},
		},
	}}
	s := &snapshot.Snapshot{
		Nodes:       []*corev1.Node{testNode("n1a", "us-east-1a")},
		Pods:        pods,
		ReplicaSets: []*appsv1.ReplicaSet{rs},
		Deployments: []*appsv1.Deployment{dep},
		PVCs: []*corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: "data-db-0", Namespace: "default"},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pv-db-0"},
		}},
		PVs: []*corev1.PersistentVolume{{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-db-0"},
			Spec: corev1.PersistentVolumeSpec{
				NodeAffinity: &corev1.VolumeNodeAffinity{
					Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
						MatchExpressions: []corev1.NodeSelectorRequirement{{
							Key:      domain.LabelZone,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"us-east-1a"},
						}},
					}}},
				},
			},
		}},
	}

	r := Analyze(s, domain.LabelZone)
	v := verdictFor(r, "us-east-1a", "db")
	if v == nil || v.Outcome != OutcomeLost {
		t.Fatalf("verdict = %+v, want OutcomeLost: the only replica is pinned to us-east-1a", v)
	}
	for _, dr := range r.Domains {
		if dr.Domain != "us-east-1a" {
			continue
		}
		if dr.Lost != 1 {
			t.Errorf("domain totals = %d lost; want exactly 1 lost", dr.Lost)
		}
	}
}

package draincheck

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/sched"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
)

const zoneKey = domain.LabelZone

// node builds a ready, schedulable node in the given zone with enough
// capacity that NodeResourcesFit never rejects it on its own.
func node(name, zone string) *corev1.Node {
	capacity := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("8"),
		corev1.ResourceMemory: resource.MustParse("32Gi"),
		corev1.ResourcePods:   resource.MustParse("110"),
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  types.UID(name),
			Labels: map[string]string{
				zoneKey:              zone,
				corev1.LabelHostname: name,
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: capacity,
			Capacity:    capacity,
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func threeZonesOneNodeEach() []*corev1.Node {
	return []*corev1.Node{node("n-a", "zone-a"), node("n-b", "zone-b"), node("n-c", "zone-c")}
}

// spreadPod builds one replica of "web", enforced 1-per-zone via a required
// topology spread constraint, on the given node.
func spreadPod(name, nodeName string) *corev1.Pod {
	controller := true
	labels := map[string]string{"app": "web"}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID(name),
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				Kind: "Deployment", Name: "web", UID: types.UID("web"), Controller: &controller,
			}},
		},
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{{Name: "app", Image: "example.test/app:latest"}},
			TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
				MaxSkew:           1,
				TopologyKey:       zoneKey,
				WhenUnsatisfiable: corev1.DoNotSchedule,
				LabelSelector:     &metav1.LabelSelector{MatchLabels: labels},
			}},
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

// threeReplicasSpreadEvenly is the exact shape spec §5.6 describes: three
// replicas, one per zone, an enforced maxSkew:1 spread, deadlocking any
// domain's drain the moment its replica is evicted.
func threeReplicasSpreadEvenly() []*corev1.Pod {
	return []*corev1.Pod{
		spreadPod("web-a", "n-a"),
		spreadPod("web-b", "n-b"),
		spreadPod("web-c", "n-c"),
	}
}

func pdbMinAvailable(name string, n int32, selector map[string]string) *policyv1.PodDisruptionBudget {
	v := intstr.FromInt32(n)
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &v,
			Selector:     &metav1.LabelSelector{MatchLabels: selector},
		},
	}
}

func snapshotWith(nodes []*corev1.Node, pods []*corev1.Pod, pdbs []*policyv1.PodDisruptionBudget) *snapshot.Snapshot {
	return &snapshot.Snapshot{TakenAt: time.Now(), Nodes: nodes, Pods: pods, PDBs: pdbs}
}

func newScheduler(t *testing.T, snap *snapshot.Snapshot) *sched.Scheduler {
	t.Helper()
	s, err := sched.New(context.Background(), snap)
	if err != nil {
		t.Fatalf("sched.New: %v", err)
	}
	return s
}

// TestDetectFindsTheSpreadPlusBudgetDeadlock is the spec §5.6 scenario this
// whole package exists for: a satisfiable budget (minAvailable:2 of 3, so one
// eviction is allowed on paper) plus an enforced 1-per-zone spread. Evicting
// any one replica leaves a replacement that cannot land in either remaining
// zone without breaking the spread, so the drain deadlocks the moment it
// starts.
func TestDetectFindsTheSpreadPlusBudgetDeadlock(t *testing.T) {
	pods := threeReplicasSpreadEvenly()
	pdb := pdbMinAvailable("web-pdb", 2, map[string]string{"app": "web"})
	snap := snapshotWith(threeZonesOneNodeEach(), pods, []*policyv1.PodDisruptionBudget{pdb})
	s := newScheduler(t, snap)

	findings, err := Detect(context.Background(), s, snap, zoneKey)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(findings) != 3 {
		// One finding per zone: whichever zone drains first, the same
		// deadlock shape recurs, and the harness's per-app check needs every
		// domain flagged, not just the first one tried.
		t.Fatalf("want 3 findings (one per zone), got %d: %+v", len(findings), findings)
	}
	for _, f := range findings {
		if f.PDB != "web-pdb" || f.Namespace != "default" {
			t.Errorf("finding does not name the budget: %+v", f)
		}
	}
}

// TestDetectSkipsAWorkloadWithNoBudget: no PDB means no eviction is ever
// refused, whatever happens to the replacement, so there is no deadlock to
// report.
func TestDetectSkipsAWorkloadWithNoBudget(t *testing.T) {
	pods := threeReplicasSpreadEvenly()
	snap := snapshotWith(threeZonesOneNodeEach(), pods, nil)
	s := newScheduler(t, snap)

	findings, err := Detect(context.Background(), s, snap, zoneKey)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want no findings without a PDB, got %+v", findings)
	}
}

// TestDetectSkipsANeverSatisfiableBudget: pdbcheck already flags this budget
// as blocking on its own; this package's whole purpose is the gap where the
// static check says "fine" and is wrong, not to duplicate a finding pdbcheck
// already produces.
func TestDetectSkipsANeverSatisfiableBudget(t *testing.T) {
	pods := threeReplicasSpreadEvenly()
	pdb := pdbMinAvailable("web-pdb", 3, map[string]string{"app": "web"}) // never satisfiable
	snap := snapshotWith(threeZonesOneNodeEach(), pods, []*policyv1.PodDisruptionBudget{pdb})
	s := newScheduler(t, snap)

	findings, err := Detect(context.Background(), s, snap, zoneKey)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want no findings for a budget pdbcheck already flags, got %+v", findings)
	}
}

// TestDetectFindsNothingWhenAReplacementCanSchedule: a spare, empty zone
// means the replacement always has somewhere to go, so there is no deadlock.
func TestDetectFindsNothingWhenAReplacementCanSchedule(t *testing.T) {
	nodes := append(threeZonesOneNodeEach(), node("n-d", "zone-d"))
	pods := threeReplicasSpreadEvenly()
	pdb := pdbMinAvailable("web-pdb", 2, map[string]string{"app": "web"})
	snap := snapshotWith(nodes, pods, []*policyv1.PodDisruptionBudget{pdb})
	s := newScheduler(t, snap)

	findings, err := Detect(context.Background(), s, snap, zoneKey)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want no findings when a replacement can schedule into the spare zone, got %+v", findings)
	}
}

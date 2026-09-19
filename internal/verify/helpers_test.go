package verify

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/fix"
	"github.com/SaiPisey2/kubectl-survive/internal/sched"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/spread"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
)

// zoneKey is the domain key every test in this package uses unless it is
// exercising a different topology key on purpose.
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
				domain.LabelZone:     zone,
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

// nodeNameForZone is this file's fixed zone-to-node naming: "zone-a" always
// has exactly one node, "n-a".
func nodeNameForZone(zone string) string {
	return "n-" + strings.TrimPrefix(zone, "zone-")
}

// threeZonesOneNodeEach is the standard three-zone, one-node-per-zone
// cluster most tests in this package run against.
func threeZonesOneNodeEach() []*corev1.Node {
	return []*corev1.Node{node("n-a", "zone-a"), node("n-b", "zone-b"), node("n-c", "zone-c")}
}

// oneUsableZoneTwoTainted is threeZonesOneNodeEach with zone-b and zone-c's
// nodes carrying a NoSchedule taint the workload does not tolerate, leaving
// zone-a as the only place anything can land.
func oneUsableZoneTwoTainted() []*corev1.Node {
	tainted := func(n *corev1.Node) *corev1.Node {
		n.Spec.Taints = []corev1.Taint{{Key: "dedicated", Value: "other", Effect: corev1.TaintEffectNoSchedule}}
		return n
	}
	return []*corev1.Node{
		node("n-a", "zone-a"),
		tainted(node("n-b", "zone-b")),
		tainted(node("n-c", "zone-c")),
	}
}

// ownedPod builds a pod on the given node, controlled by a Deployment named
// "workload" in namespace "default" -- the same identity verdictFor and
// inputFor use, so real pods in a test snapshot are grouped as the same
// workload the fix pipeline reasons about.
func ownedPod(name, node string, labels map[string]string) *corev1.Pod {
	controller := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID(name),
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				Kind:       "Deployment",
				Name:       "workload",
				UID:        types.UID("workload"),
				Controller: &controller,
			}},
		},
		Spec: corev1.PodSpec{
			NodeName:   node,
			Containers: []corev1.Container{{Name: "app", Image: "example.test/app:latest"}},
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

// threeReplicasAllIn builds three replicas of the "web" workload, all
// crammed onto the single node in the given zone.
func threeReplicasAllIn(zone string) []*corev1.Pod {
	n := nodeNameForZone(zone)
	labels := map[string]string{"app": "web"}
	return []*corev1.Pod{
		ownedPod("web-0", n, labels),
		ownedPod("web-1", n, labels),
		ownedPod("web-2", n, labels),
	}
}

// oneReplicaIn builds the single replica of the "singleton" workload, in the
// given zone.
func oneReplicaIn(zone string) []*corev1.Pod {
	return []*corev1.Pod{ownedPod("singleton-0", nodeNameForZone(zone), map[string]string{"app": "singleton"})}
}

// webTemplate is a bare pod template for the "web" workload: no spread
// constraint, no affinity, nothing that would make a rung fire on its own.
func webTemplate() *corev1.PodTemplateSpec {
	return &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "example.test/app:latest"}},
		},
	}
}

// singletonTemplate is webTemplate's counterpart for the "singleton"
// workload used by the partial-fix (PDB repair) test.
func singletonTemplate() *corev1.PodTemplateSpec {
	return &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "singleton"}},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "example.test/app:latest"}},
		},
	}
}

// templateSelectingAMissingNodeLabel carries a node selector no node in this
// package's fixtures ever satisfies, so every node rejects it in Filter.
func templateSelectingAMissingNodeLabel() *corev1.PodTemplateSpec {
	t := webTemplate()
	t.Spec.NodeSelector = map[string]string{"disktype": "ssd-that-does-not-exist"}
	return t
}

// snapshotWith constructs a snapshot.Snapshot directly from nodes and pods.
func snapshotWith(nodes []*corev1.Node, pods []*corev1.Pod) *snapshot.Snapshot {
	return &snapshot.Snapshot{TakenAt: time.Now(), Nodes: nodes, Pods: pods}
}

// newVerifier builds a real framework over snap and wraps it in a Verifier,
// failing the test immediately if the framework cannot be built.
func newVerifier(t *testing.T, snap *snapshot.Snapshot) *Verifier {
	t.Helper()
	s, err := sched.New(context.Background(), snap)
	if err != nil {
		t.Fatalf("sched.New: %v", err)
	}
	return NewVerifier(s, snap, zoneKey)
}

// inputFor is the common case across this file's tests: a template, a
// spread state, and a replica count, spread across three zones with the
// template's own labels as the selector and as the identifying workload
// "Deployment/default/workload".
func inputFor(tmpl *corev1.PodTemplateSpec, state spread.State, replicas int) fix.Input {
	v := survive.Verdict{
		Workload: workload.Ref{Kind: "Deployment", Namespace: "default", Name: "workload"},
		Outcome:  survive.OutcomeLost,
		Spread:   spread.Assessment{State: state},
	}
	v.AntiAffinity = spread.ClassifyAntiAffinity(tmpl.Spec.Affinity, zoneKey)
	return fix.Input{
		Verdict:   v,
		Template:  tmpl,
		Replicas:  replicas,
		DomainKey: zoneKey,
		Domains:   []string{"zone-a", "zone-b", "zone-c"},
		Selector:  tmpl.ObjectMeta.Labels,
	}
}

// pdbMinAvailable builds a PodDisruptionBudget carrying only a minAvailable
// field.
func pdbMinAvailable(n int32) *policyv1.PodDisruptionBudget {
	v := intstr.FromInt32(n)
	return &policyv1.PodDisruptionBudget{Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &v}}
}

// mustFindRung returns the fix at the given rung, failing the test when it
// was not generated.
func mustFindRung(t *testing.T, fixes []fix.Fix, r fix.Rung) fix.Fix {
	t.Helper()
	for i := range fixes {
		if fixes[i].Rung == r {
			return fixes[i]
		}
	}
	t.Fatalf("rung %d was not generated", r)
	return fix.Fix{}
}

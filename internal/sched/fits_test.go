package sched

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFitsRejectsTheOverfullDomainUnderAnEnforcedSpread(t *testing.T) {
	// Two app=web replicas already sit in zone-a. A maxSkew=1 zone spread must
	// put the third anywhere but zone-a.
	s := mustNew(t, snapshotWith(
		[]*corev1.Node{node("n-a", "zone-a"), node("n-b", "zone-b"), node("n-c", "zone-c")},
		[]*corev1.Pod{
			assignedPod("web-1", "n-a", map[string]string{"app": "web"}),
			assignedPod("web-2", "n-a", map[string]string{"app": "web"}),
		},
	))

	candidate := unassignedPod("web-3", map[string]string{"app": "web"})
	candidate.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
		MaxSkew:           1,
		TopologyKey:       zoneKey,
		WhenUnsatisfiable: corev1.DoNotSchedule,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
	}}

	fits, err := s.Fits(context.Background(), candidate, zoneKey)
	if err != nil {
		t.Fatalf("Fits: %v", err)
	}
	got := map[string]bool{}
	for _, f := range fits {
		got[f.Node] = f.OK
	}
	if got["n-a"] {
		t.Error("zone-a already holds every replica; an enforced maxSkew=1 must reject it")
	}
	if !got["n-b"] || !got["n-c"] {
		t.Errorf("the empty zones must accept the replica, got %v", got)
	}
}

func TestFitsReportsWhyANodeWasRejected(t *testing.T) {
	s := mustNew(t, snapshotWith([]*corev1.Node{node("n-a", "zone-a")}, nil))
	candidate := unassignedPod("big", nil)
	candidate.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("64"),
	}
	fits, err := s.Fits(context.Background(), candidate, zoneKey)
	if err != nil {
		t.Fatalf("Fits: %v", err)
	}
	if len(fits) != 1 || fits[0].OK {
		t.Fatalf("a 64-core request must not fit an 8-core node: %+v", fits)
	}
	if fits[0].Reason == "" {
		t.Error("a rejection with no reason is not an answer; the plugin message must be carried through")
	}
}

func TestFitsRejectsAPodThatIsAlreadyAssigned(t *testing.T) {
	// Filtering an assigned pod would double-count it against itself. The caller
	// always has a bug here, so it is an error rather than a quiet correction.
	s := mustNew(t, snapshotWith([]*corev1.Node{node("n-a", "zone-a")}, nil))
	if _, err := s.Fits(context.Background(), assignedPod("x", "n-a", nil), zoneKey); err == nil {
		t.Fatal("expected an error for an already-assigned pod")
	}
}

func TestFitsFailsClosedWhenPreFilterRejectsThePod(t *testing.T) {
	// PreFilter rejects the pod itself, not a node. Reporting that as "no node
	// fits" would be indistinguishable from a full cluster, so it is an error.
	s := mustNew(t, snapshotWith([]*corev1.Node{node("n-a", "zone-a")}, nil))
	candidate := unassignedPod("bad", nil)
	candidate.Spec.NodeSelector = map[string]string{"nonexistent": "label"}
	fits, err := s.Fits(context.Background(), candidate, zoneKey)
	if err == nil && len(fits) > 0 && fits[0].OK {
		t.Fatal("a pod no node can satisfy must never report a fit")
	}
}

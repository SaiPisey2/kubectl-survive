package sched

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPlaceSpreadsAcrossDomainsUnderAnEnforcedConstraint(t *testing.T) {
	s := mustNew(t, snapshotWith(
		[]*corev1.Node{node("n-a", "zone-a"), node("n-b", "zone-b"), node("n-c", "zone-c")}, nil))

	tmpl := unassignedPod("web", map[string]string{"app": "web"})
	tmpl.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
		MaxSkew:           1,
		TopologyKey:       zoneKey,
		WhenUnsatisfiable: corev1.DoNotSchedule,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
	}}

	p, err := s.Place(context.Background(), tmpl, 3, zoneKey)
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if p.Unschedulable != 0 {
		t.Fatalf("three replicas over three empty zones must all schedule: %+v", p)
	}
	for _, z := range []string{"zone-a", "zone-b", "zone-c"} {
		if p.Domains[z] != 1 {
			t.Errorf("zone %s holds %d replicas, want 1 (%+v)", z, p.Domains[z], p.Domains)
		}
	}
}

func TestPlaceFeedsEachPlacementBackBeforePlacingTheNext(t *testing.T) {
	// One node per zone and an enforced maxSkew=1: without feeding placements
	// back, every replica independently picks the same node and the result is
	// 3/0/0 rather than 1/1/1. This test is the whole point of the loop.
	s := mustNew(t, snapshotWith(
		[]*corev1.Node{node("n-a", "zone-a"), node("n-b", "zone-b"), node("n-c", "zone-c")}, nil))
	tmpl := withEnforcedZoneSpread(unassignedPod("web", map[string]string{"app": "web"}))

	p, err := s.Place(context.Background(), tmpl, 3, zoneKey)
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if len(p.Domains) != 3 {
		t.Fatalf("placements were not fed back: %+v", p.Domains)
	}
}

func TestPlaceReportsUnschedulableReplicasRatherThanFailing(t *testing.T) {
	// One node, an enforced hostname anti-affinity, two replicas: the second has
	// nowhere to go. That is an answer the fix ladder needs, not an error.
	s := mustNew(t, snapshotWith([]*corev1.Node{node("n-a", "zone-a")}, nil))
	tmpl := withRequiredHostnameAntiAffinity(unassignedPod("web", map[string]string{"app": "web"}))

	p, err := s.Place(context.Background(), tmpl, 2, zoneKey)
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if p.Unschedulable != 1 {
		t.Fatalf("Unschedulable = %d, want 1 (%+v)", p.Unschedulable, p)
	}
	if p.Reason == "" {
		t.Error("an unschedulable replica must carry the scheduler's reason")
	}
}

func TestPlaceRejectsANonPositiveReplicaCount(t *testing.T) {
	s := mustNew(t, snapshotWith([]*corev1.Node{node("n-a", "zone-a")}, nil))
	if _, err := s.Place(context.Background(), unassignedPod("web", nil), 0, zoneKey); err == nil {
		t.Fatal("expected an error for zero replicas")
	}
}

// TestPlaceRestoresTheClusterViewBetweenCalls is the brief's minimum
// restoration check: the fix ladder calls Place repeatedly, so a second call
// on the same Scheduler must see the real cluster, not the leftovers of the
// first.
func TestPlaceRestoresTheClusterViewBetweenCalls(t *testing.T) {
	s := mustNew(t, snapshotWith(
		[]*corev1.Node{node("n-a", "zone-a"), node("n-b", "zone-b"), node("n-c", "zone-c")}, nil))
	tmpl := withEnforcedZoneSpread(unassignedPod("web", map[string]string{"app": "web"}))

	first, err := s.Place(context.Background(), tmpl, 3, zoneKey)
	if err != nil {
		t.Fatalf("first Place: %v", err)
	}
	second, err := s.Place(context.Background(), tmpl, 3, zoneKey)
	if err != nil {
		t.Fatalf("second Place: %v", err)
	}
	if !reflect.DeepEqual(first.Domains, second.Domains) {
		t.Fatalf("second call saw a different cluster: first=%+v second=%+v", first.Domains, second.Domains)
	}
	if first.Unschedulable != second.Unschedulable {
		t.Fatalf("first.Unschedulable=%d second.Unschedulable=%d", first.Unschedulable, second.Unschedulable)
	}
}

// TestPlaceRestoresTheClusterViewAfterUnschedulableReplicas exercises
// restoration on the path where the loop keeps going past a rejection rather
// than returning early: with a required hostname anti-affinity and a single
// node, the second of two replicas is simulated as unschedulable but the
// first was genuinely placed and fed back, and must still be withdrawn.
func TestPlaceRestoresTheClusterViewAfterUnschedulableReplicas(t *testing.T) {
	s := mustNew(t, snapshotWith([]*corev1.Node{node("n-a", "zone-a")}, nil))

	before, err := s.Fits(context.Background(), unassignedPod("probe", nil), zoneKey)
	if err != nil {
		t.Fatalf("Fits before: %v", err)
	}

	tmpl := withRequiredHostnameAntiAffinity(unassignedPod("web", map[string]string{"app": "web"}))
	if _, err := s.Place(context.Background(), tmpl, 2, zoneKey); err != nil {
		t.Fatalf("Place: %v", err)
	}

	after, err := s.Fits(context.Background(), unassignedPod("probe", nil), zoneKey)
	if err != nil {
		t.Fatalf("Fits after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("cluster view was not restored: before=%+v after=%+v", before, after)
	}
}

// TestWithdrawSurfacesCacheErrors is a white-box check on withdraw itself:
// Place's deferred cleanup must not swallow a cache error, since a cache left
// holding a phantom pod would silently corrupt every later answer. Asking the
// cache to remove a pod it never saw is guaranteed to fail.
func TestWithdrawSurfacesCacheErrors(t *testing.T) {
	s := mustNew(t, snapshotWith([]*corev1.Node{node("n-a", "zone-a")}, nil))
	stray := assignedPod("stray", "n-a", nil)
	if err := s.withdraw([]*corev1.Pod{stray}); err == nil {
		t.Fatal("expected an error removing a pod the cache never saw")
	}
}

// withEnforcedZoneSpread attaches an enforced maxSkew=1 zone spread
// constraint matching the pod's own labels, used to prove that feedback
// between replica placements is real.
func withEnforcedZoneSpread(pod *corev1.Pod) *corev1.Pod {
	pod.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
		MaxSkew:           1,
		TopologyKey:       zoneKey,
		WhenUnsatisfiable: corev1.DoNotSchedule,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: pod.Labels},
	}}
	return pod
}

// withRequiredHostnameAntiAffinity attaches a required pod anti-affinity over
// the hostname topology, matching the pod's own labels, so a second replica
// cannot share a node with the first.
func withRequiredHostnameAntiAffinity(pod *corev1.Pod) *corev1.Pod {
	pod.Spec.Affinity = &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
				TopologyKey:   corev1.LabelHostname,
				LabelSelector: &metav1.LabelSelector{MatchLabels: pod.Labels},
			}},
		},
	}
	return pod
}

package verify

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/SaiPisey2/kubectl-survive/internal/fix"
	"github.com/SaiPisey2/kubectl-survive/internal/spread"
)

// capped builds a node whose pod capacity is exactly n.
func capped(name, zone string, n int64) *corev1.Node {
	nd := node(name, zone)
	nd.Status.Allocatable[corev1.ResourcePods] = *resource.NewQuantity(n, resource.DecimalSI)
	nd.Status.Capacity[corev1.ResourcePods] = *resource.NewQuantity(n, resource.DecimalSI)
	return nd
}

// A fix to an existing workload is a rollout: its old pods are replaced, not
// kept alongside a second copy. The schedulability proof must simulate that
// steady state -- the workload's own existing pods withdrawn first -- rather
// than adding the new replicas on top of a cluster that still holds them.
//
// Cluster: zone-a holds exactly 3 pods (full), zone-b and zone-c hold 1 each.
// Workload: 3 replicas, all currently in zone-a.
// Fix: enforced maxSkew=1 on zone.
// After the rollout completes there is 1 replica per zone, which fits
// exactly. A proof that counts the workload against itself would instead
// need 3 free slots on top of the 3 already used, find only 2, and wrongly
// refuse a fix that works.
func TestVerifySimulatesTheRolloutNotTheAddition(t *testing.T) {
	nodes := []*corev1.Node{
		capped("n-a", "zone-a", 3),
		capped("n-b", "zone-b", 1),
		capped("n-c", "zone-c", 1),
	}
	snap := snapshotWith(nodes, threeReplicasAllIn("zone-a"))
	v := newVerifier(t, snap)

	in := inputFor(webTemplate(), spread.StateAbsent, 3)
	f := mustFindRung(t, fix.Candidates(in), fix.RungSpreadAdd)

	p := v.Verify(context.Background(), in, f)
	if p.Err != nil {
		t.Fatalf("Verify: %v", p.Err)
	}
	if !p.Schedulable {
		t.Fatalf("the rollout fits exactly one replica per zone; counting the old pods too "+
			"wrongly refuses a working fix: %s", p.SchedulableDetail)
	}
	if p.SchedulableDetail != "zone-a:1 zone-b:1 zone-c:1" {
		t.Fatalf("want the true post-rollout placement zone-a:1 zone-b:1 zone-c:1, got %q", p.SchedulableDetail)
	}
	if !p.Survives {
		t.Fatalf("one replica per zone survives losing any single zone: %s", p.SurvivesDetail)
	}
}

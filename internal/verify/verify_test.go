package verify

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/SaiPisey2/kubectl-survive/internal/fix"
	"github.com/SaiPisey2/kubectl-survive/internal/spread"
)

// These run against a real framework over synthetic nodes, not against
// mocks. A mocked scheduler would prove only that the mock agrees with
// itself.

func TestVerifyAcceptsASpreadFixThatSchedulesAndSurvives(t *testing.T) {
	// Three replicas crammed into zone-a, three zones with room. Adding an
	// enforced maxSkew=1 must both schedule and survive losing zone-a.
	snap := snapshotWith(threeZonesOneNodeEach(), threeReplicasAllIn("zone-a"))
	v := newVerifier(t, snap)

	in := inputFor(webTemplate(), spread.StateAbsent, 3)
	f := mustFindRung(t, fix.Candidates(in), fix.RungSpreadAdd)

	p := v.Verify(context.Background(), in, f)
	if p.Err != nil {
		t.Fatalf("Verify: %v", p.Err)
	}
	if !p.Schedulable {
		t.Fatalf("three replicas over three empty zones must schedule: %s", p.SchedulableDetail)
	}
	if !p.Survives {
		t.Fatalf("adding an enforced spread constraint must survive losing zone-a: %s", p.SurvivesDetail)
	}
}

func TestVerifyRejectsAFixThatCannotSchedule(t *testing.T) {
	// One node per zone, but two zones are cordoned by a taint the workload
	// does not tolerate. An enforced maxSkew=1 over three replicas then has
	// nowhere to put two of them. This is the outage the tool exists to
	// prevent, so the fix must be refused rather than printed with a caveat.
	snap := snapshotWith(oneUsableZoneTwoTainted(), threeReplicasAllIn("zone-a"))
	v := newVerifier(t, snap)

	in := inputFor(webTemplate(), spread.StateAbsent, 3)
	f := mustFindRung(t, fix.Candidates(in), fix.RungSpreadAdd)

	p := v.Verify(context.Background(), in, f)
	if p.Schedulable {
		t.Fatal("two replicas have nowhere to go; claiming schedulable would cause the outage")
	}
	if p.SchedulableDetail == "" {
		t.Error("a refusal must carry the scheduler's reason")
	}
	if p.Survives {
		t.Fatal("a fix that cannot schedule cannot survive anything")
	}
}

func TestVerifyReportsAPartialFixAsPartial(t *testing.T) {
	// Repairing an unsatisfiable budget unblocks drains without moving a
	// single replica out of its zone. Schedulable yes, survives no.
	snap := snapshotWith(threeZonesOneNodeEach(), oneReplicaIn("zone-a"))
	v := newVerifier(t, snap)

	in := inputFor(singletonTemplate(), spread.StateAbsent, 1)
	in.PDB = pdbMinAvailable(1)
	f := mustFindRung(t, fix.Candidates(in), fix.RungPDBRepair)

	p := v.Verify(context.Background(), in, f)
	if !p.Schedulable {
		t.Fatalf("a budget change moves no pods: %s", p.SchedulableDetail)
	}
	if p.Survives {
		t.Fatal("the replica is still alone in zone-a; this fix is partial and must say so")
	}
}

func TestVerifyReportsAnErrorRatherThanGuessing(t *testing.T) {
	// A template the scheduler rejects outright is not evidence about the
	// fix.
	snap := snapshotWith(threeZonesOneNodeEach(), nil)
	v := newVerifier(t, snap)
	in := inputFor(templateSelectingAMissingNodeLabel(), spread.StateAbsent, 2)

	p := v.Verify(context.Background(), in, fix.Fix{
		Rung:                  fix.RungSpreadAdd,
		Mutate:                func(*corev1.PodTemplateSpec) {},
		ImprovesSurvivability: true,
	})
	if p.Err == nil && p.Schedulable {
		t.Fatal("an unanswerable question must not be answered yes")
	}
	if p.Err != nil && (p.Schedulable || p.Survives) {
		t.Fatal("an errored proof must claim nothing")
	}
}

func TestVerifyLeavesTheClusterViewUnchanged(t *testing.T) {
	// Verification simulates placements. If it does not withdraw them, the
	// second fix is verified against a cluster that never existed.
	snap := snapshotWith(threeZonesOneNodeEach(), threeReplicasAllIn("zone-a"))
	v := newVerifier(t, snap)
	in := inputFor(webTemplate(), spread.StateAbsent, 3)
	f := mustFindRung(t, fix.Candidates(in), fix.RungSpreadAdd)

	first := v.Verify(context.Background(), in, f)
	second := v.Verify(context.Background(), in, f)
	if first.Schedulable != second.Schedulable || first.Survives != second.Survives {
		t.Fatalf("verification is not repeatable: %+v then %+v", first, second)
	}
}

func TestVerifyRejectsAFixThatChangesNothing(t *testing.T) {
	// A fix with no template mutation, no replica change, and no PDB rung
	// alters nothing the proofs observe. Claiming that proves anything would
	// be a checkbox generator dressed up as a proof.
	snap := snapshotWith(threeZonesOneNodeEach(), threeReplicasAllIn("zone-a"))
	v := newVerifier(t, snap)
	in := inputFor(webTemplate(), spread.StateAbsent, 3)

	p := v.Verify(context.Background(), in, fix.Fix{Rung: fix.RungSpreadAdd, ImprovesSurvivability: true})
	if p.Err == nil {
		t.Fatal("a fix that changes nothing must be rejected explicitly, not silently passed")
	}
	if p.Schedulable || p.Survives {
		t.Fatal("a rejected fix must claim nothing")
	}
}

func TestVerifyArchitecturalFindingProvesNothing(t *testing.T) {
	// Rung 8 is a reported finding, never a patch. Verify must not pretend to
	// have checked something it was never asked to check.
	snap := snapshotWith(threeZonesOneNodeEach(), threeReplicasAllIn("zone-a"))
	v := newVerifier(t, snap)
	in := inputFor(webTemplate(), spread.StateAbsent, 3)

	p := v.Verify(context.Background(), in, fix.Fix{Rung: fix.RungVolumePin, Architectural: "zonal volume"})
	if p.Err != nil || p.Schedulable || p.Survives {
		t.Fatalf("an architectural finding must prove nothing, got %+v", p)
	}
}

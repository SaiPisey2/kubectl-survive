package fix

import (
	"strings"
	"testing"

	"github.com/SaiPisey2/kubectl-survive/internal/spread"
	corev1 "k8s.io/api/core/v1"
)

func TestRung1AddsAnEnforcedSpreadWhenThereIsNone(t *testing.T) {
	got := Candidates(Input{
		Verdict:   verdictWithSpread(spread.StateAbsent),
		Template:  templateWithLabels(map[string]string{"app": "web"}),
		Replicas:  3,
		DomainKey: zoneKey,
		Domains:   []string{"zone-a", "zone-b", "zone-c"},
		Selector:  map[string]string{"app": "web"},
	})
	f := mustFindRung(t, got, RungSpreadAdd)

	applied := f.Apply(templateWithLabels(map[string]string{"app": "web"}))
	cs := applied.Spec.TopologySpreadConstraints
	if len(cs) != 1 {
		t.Fatalf("want exactly one constraint, got %d", len(cs))
	}
	if cs[0].MaxSkew != 1 || cs[0].TopologyKey != zoneKey || cs[0].WhenUnsatisfiable != corev1.DoNotSchedule {
		t.Fatalf("a weak constraint is not a fix: %+v", cs[0])
	}
	if cs[0].LabelSelector == nil {
		t.Fatal("a constraint with no selector counts every pod in the namespace and is not the fix we described")
	}
}

func TestRung1IsNotOfferedForASingleReplica(t *testing.T) {
	// Spreading one replica across three zones is meaningless; rung 4 is the
	// honest answer there.
	got := Candidates(Input{
		Verdict: verdictWithSpread(spread.StateAbsent), Template: templateWithLabels(nil),
		Replicas: 1, DomainKey: zoneKey, Domains: []string{"zone-a", "zone-b"},
	})
	if findRung(got, RungSpreadAdd) != nil {
		t.Fatal("a single replica cannot be spread")
	}
}

func TestRung2EnforcesAnAdvisoryConstraintWithoutAddingAnother(t *testing.T) {
	tmpl := templateWithSpread(corev1.ScheduleAnyway, 1)
	got := Candidates(inputFor(tmpl, spread.StateAdvisory, 3))
	f := mustFindRung(t, got, RungSpreadEnforce)

	applied := f.Apply(tmpl)
	if n := len(applied.Spec.TopologySpreadConstraints); n != 1 {
		t.Fatalf("rung 2 must convert the existing constraint, not add one: got %d", n)
	}
	if applied.Spec.TopologySpreadConstraints[0].WhenUnsatisfiable != corev1.DoNotSchedule {
		t.Fatal("rung 2 did not enforce the constraint")
	}
	if findRung(got, RungSpreadAdd) != nil {
		t.Fatal("rung 1 must not also fire when a constraint already exists")
	}
}

func TestRung2OnlyTouchesConstraintsForTheDomainKeyUnderTest(t *testing.T) {
	// A hostname spread is a different guarantee. Rewriting it would change
	// behaviour the operator never asked about.
	tmpl := templateWithSpreadKey("kubernetes.io/hostname", corev1.ScheduleAnyway, 1)
	got := Candidates(inputFor(tmpl, spread.StateAbsent, 3))
	if f := findRung(got, RungSpreadEnforce); f != nil {
		t.Fatal("a constraint on another topology key must be left alone")
	}
}

func TestRung3LowersMaxSkewToOne(t *testing.T) {
	tmpl := templateWithSpread(corev1.DoNotSchedule, 3)
	got := Candidates(inputFor(tmpl, spread.StateEnforcedButWeak, 3))
	f := mustFindRung(t, got, RungSpreadTighten)
	if applied := f.Apply(tmpl); applied.Spec.TopologySpreadConstraints[0].MaxSkew != 1 {
		t.Fatal("rung 3 did not tighten maxSkew")
	}
}

func TestRung4RaisesReplicasToTheDomainCount(t *testing.T) {
	got := Candidates(Input{
		Verdict: verdictWithSpread(spread.StateAbsent), Template: templateWithLabels(nil),
		Replicas: 1, DomainKey: zoneKey, Domains: []string{"zone-a", "zone-b", "zone-c"},
	})
	f := mustFindRung(t, got, RungReplicasRaise)
	if !strings.Contains(f.Title, "3") {
		t.Fatalf("the title must name the target replica count: %q", f.Title)
	}
	if f.Mutate != nil {
		t.Fatal("replica count does not live in the pod template; rung 4 carries a patch, not a template mutation")
	}
}

func TestRung4IsNotOfferedWhenThereAreAlreadyEnoughReplicas(t *testing.T) {
	got := Candidates(Input{
		Verdict: verdictWithSpread(spread.StateAbsent), Template: templateWithLabels(nil),
		Replicas: 5, DomainKey: zoneKey, Domains: []string{"zone-a", "zone-b", "zone-c"},
	})
	if findRung(got, RungReplicasRaise) != nil {
		t.Fatal("5 replicas already exceed 3 domains")
	}
}

func TestCandidatesAreReturnedInRungOrder(t *testing.T) {
	got := Candidates(inputFor(templateWithSpread(corev1.ScheduleAnyway, 5), spread.StateAdvisory, 1))
	for i := 1; i < len(got); i++ {
		if got[i-1].Rung > got[i].Rung {
			t.Fatalf("rungs out of order: %v", rungsOf(got))
		}
	}
}

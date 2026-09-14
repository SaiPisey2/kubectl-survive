package fix

import (
	"strings"
	"testing"

	"github.com/SaiPisey2/kubectl-survive/internal/spread"
	"github.com/SaiPisey2/kubectl-survive/internal/volumepin"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
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

func TestRung1DoesNotFireForAWorkloadWithNoIdentifyingLabels(t *testing.T) {
	// An empty-but-present LabelSelector matches every pod in the namespace,
	// not none. Offering rung 1 here would hand the operator a patch that
	// silently captures every unrelated workload sharing the namespace — a
	// far more disruptive change than the one being recommended. A workload
	// with no identifying labels cannot be given a safe spread constraint at
	// all, so the rung must not fire.
	got := Candidates(Input{
		Verdict: verdictWithSpread(spread.StateAbsent), Template: templateWithLabels(nil),
		Replicas: 3, DomainKey: zoneKey, Domains: []string{"zone-a", "zone-b", "zone-c"},
		Selector: nil,
	})
	if findRung(got, RungSpreadAdd) != nil {
		t.Fatal("rung 1 must not fire without an identifying selector; it would generate an empty LabelSelector matching every pod in the namespace")
	}

	got = Candidates(Input{
		Verdict: verdictWithSpread(spread.StateAbsent), Template: templateWithLabels(nil),
		Replicas: 3, DomainKey: zoneKey, Domains: []string{"zone-a", "zone-b", "zone-c"},
		Selector: map[string]string{},
	})
	if findRung(got, RungSpreadAdd) != nil {
		t.Fatal("rung 1 must not fire for an empty (but non-nil) selector either")
	}
}

func TestPatchesNeverContainGoMapFormatting(t *testing.T) {
	inputs := []Input{
		inputFor(templateWithLabels(map[string]string{"app": "web", "env": "prod", "tier": "fe"}), spread.StateAbsent, 3),
		inputFor(templateWithSpread(corev1.ScheduleAnyway, 5), spread.StateAdvisory, 3),
		inputFor(templateWithSpread(corev1.DoNotSchedule, 3), spread.StateEnforcedButWeak, 3),
		{
			Verdict: verdictWithSpread(spread.StateAbsent), Template: templateWithLabels(nil),
			Replicas: 1, DomainKey: zoneKey, Domains: []string{"zone-a", "zone-b", "zone-c"},
		},
		func() Input {
			in := inputFor(templateWithLabels(map[string]string{"app": "s"}), spread.StateAbsent, 1)
			in.PDB = pdbMinAvailable(1)
			return in
		}(),
		inputFor(templateWithPreferredZoneAntiAffinity(), spread.StateAbsent, 3),
	}
	for _, in := range inputs {
		for _, f := range Candidates(in) {
			if strings.Contains(f.Patch, "map[") {
				t.Fatalf("rung %d patch contains Go map formatting, not YAML:\n%s", f.Rung, f.Patch)
			}
		}
	}
}

func TestRung1PatchRendersEachLabelOnItsOwnLineInSortedOrder(t *testing.T) {
	got := Candidates(Input{
		Verdict:   verdictWithSpread(spread.StateAbsent),
		Template:  templateWithLabels(map[string]string{"tier": "fe", "app": "web", "env": "prod"}),
		Replicas:  3,
		DomainKey: zoneKey,
		Domains:   []string{"zone-a", "zone-b", "zone-c"},
		Selector:  map[string]string{"tier": "fe", "app": "web", "env": "prod"},
	})
	f := mustFindRung(t, got, RungSpreadAdd)

	want := []string{"app: web", "env: prod", "tier: fe"}
	lastIdx := -1
	for _, w := range want {
		idx := strings.Index(f.Patch, w)
		if idx == -1 {
			t.Fatalf("patch missing label line %q:\n%s", w, f.Patch)
		}
		if idx <= lastIdx {
			t.Fatalf("label lines are not in sorted order in patch:\n%s", f.Patch)
		}
		lastIdx = idx
	}
}

// finalYAML strips the unified-diff marker byte from every kept (' ' or '+')
// line of a patch, discarding removed ('-') lines, to reproduce the document
// that results after the patch is applied.
func finalYAML(patch string) string {
	var out []string
	for _, line := range strings.Split(strings.TrimRight(patch, "\n"), "\n") {
		if line == "" {
			continue
		}
		marker, rest := line[0], line[1:]
		if marker == '-' {
			continue
		}
		out = append(out, rest)
	}
	return strings.Join(out, "\n")
}

func TestRung5AddsASatisfiableBudget(t *testing.T) {
	in := inputFor(templateWithLabels(map[string]string{"app": "web"}), spread.StateEnforced, 3)
	in.PDB = nil
	f := mustFindRung(t, Candidates(in), RungPDBAdd)
	if !strings.Contains(f.Patch, "maxUnavailable") {
		t.Fatalf("prefer maxUnavailable: it stays satisfiable as replicas change: %q", f.Patch)
	}
	if f.ImprovesSurvivability {
		t.Fatal("a PDB does not protect against a zone vanishing; claiming otherwise is the dishonesty spec 6.2 forbids")
	}
}

func TestRung6RepairsAnUnsatisfiableBudget(t *testing.T) {
	// minAvailable=1 with replicas=1 can never be satisfied: no pod is ever
	// evictable and any drain touching it hangs forever.
	in := inputFor(templateWithLabels(map[string]string{"app": "s"}), spread.StateAbsent, 1)
	in.PDB = pdbMinAvailable(1)
	fixes := Candidates(in)

	repair := mustFindRung(t, fixes, RungPDBRepair)
	if !strings.Contains(repair.Patch, "maxUnavailable") {
		t.Fatalf("the repair must replace minAvailable with a satisfiable budget: %q", repair.Patch)
	}
	if repair.ImprovesSurvivability {
		t.Fatal("repairing a budget unblocks drains; it does not make a single-zone workload survive")
	}

	// Raising replicas is the fix that does both, and must be offered too.
	raise := mustFindRung(t, fixes, RungReplicasRaise)
	if !raise.ImprovesSurvivability {
		t.Fatal("more replicas across more domains is a survivability fix")
	}
}

func TestRung5IsNotOfferedWhenABudgetAlreadyExists(t *testing.T) {
	in := inputFor(templateWithLabels(nil), spread.StateEnforced, 3)
	in.PDB = pdbMaxUnavailable(1)
	if findRung(Candidates(in), RungPDBAdd) != nil {
		t.Fatal("rung 5 must not duplicate an existing budget")
	}
}

// The same trap rung 1 was fixed to avoid: an empty-but-present selector on
// a PDB matches every pod in the namespace, and an unsatisfiable one hangs
// every drain touching any of those pods, not just the workload the
// operator meant to help. Rung 5 must refuse to fire without identifying
// labels, exactly like rung 1.
func TestRung5DoesNotFireForAWorkloadWithNoIdentifyingLabels(t *testing.T) {
	in := inputFor(templateWithLabels(nil), spread.StateEnforced, 3)
	in.Selector = nil
	if findRung(Candidates(in), RungPDBAdd) != nil {
		t.Fatal("rung 5 must not fire without an identifying selector; it would generate a PDB matching every pod in the namespace")
	}

	in.Selector = map[string]string{}
	if findRung(Candidates(in), RungPDBAdd) != nil {
		t.Fatal("rung 5 must not fire for an empty (but non-nil) selector either")
	}
}

func TestRung7PromotesPreferredAntiAffinityToRequired(t *testing.T) {
	tmpl := templateWithPreferredZoneAntiAffinity()
	f := mustFindRung(t, Candidates(inputFor(tmpl, spread.StateAbsent, 3)), RungAntiAffinity)

	applied := f.Apply(tmpl)
	req := applied.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	pref := applied.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if len(req) != 1 {
		t.Fatalf("the preferred term must become a required one, got %d", len(req))
	}
	if len(pref) != 0 {
		t.Fatal("the preferred term must be removed, not duplicated: keeping both double-counts the constraint")
	}
	if req[0].TopologyKey != zoneKey {
		t.Fatalf("topology key changed: %q", req[0].TopologyKey)
	}
	if !f.ImprovesSurvivability {
		t.Fatal("a required anti-affinity term is a real scheduling guarantee: this is a survivability fix")
	}
}

func TestRung8IsReportedAndNeverPatched(t *testing.T) {
	in := inputFor(templateWithLabels(nil), spread.StateEnforced, 3)
	in.Verdict.VolumePins = []volumepin.Pin{{PVC: "data-0", PV: "pvc-8f21ac", Domain: "zone-a"}}
	f := mustFindRung(t, Candidates(in), RungVolumePin)

	if f.Fixable() {
		t.Fatal("moving a zonal disk is a data migration, not a patch")
	}
	if f.Patch != "" || f.Mutate != nil {
		t.Fatal("rung 8 must carry no patch and no mutation")
	}
	if f.ImprovesSurvivability {
		t.Fatal("rung 8 is a reported finding, not a fix; it must not claim to improve survivability")
	}
	for _, want := range []string{"pvc-8f21ac", "zone-a"} {
		if !strings.Contains(f.Architectural, want) {
			t.Errorf("the finding must name %q so an operator can act on it: %q", want, f.Architectural)
		}
	}
}

func TestRung8SuppressesPatchRungsThatCannotHelp(t *testing.T) {
	// A pod pinned to a zone by its volume cannot be spread out of that zone.
	// Offering a spread constraint would be a fix that provably cannot work,
	// and the scheduler proof would reject it anyway - but offering it at all
	// wastes the operator's attention.
	in := inputFor(templateWithLabels(nil), spread.StateAbsent, 1)
	in.Verdict.VolumePins = []volumepin.Pin{{PVC: "data-0", PV: "pv-1", Domain: "zone-a"}}
	got := Candidates(in)
	if findRung(got, RungSpreadAdd) != nil {
		t.Fatal("a zone-pinned pod cannot be spread")
	}
}

func TestRung8SuppressesAntiAffinityRungThatCannotHelp(t *testing.T) {
	// A zone-pinned pod cannot be moved by an anti-affinity rule either.
	tmpl := templateWithPreferredZoneAntiAffinity()
	in := inputFor(tmpl, spread.StateAbsent, 3)
	in.Verdict.VolumePins = []volumepin.Pin{{PVC: "data-0", PV: "pv-1", Domain: "zone-a"}}
	got := Candidates(in)
	if findRung(got, RungAntiAffinity) != nil {
		t.Fatal("a zone-pinned pod cannot be helped by anti-affinity either")
	}
	if findRung(got, RungVolumePin) == nil {
		t.Fatal("the volume pin finding must still be reported")
	}
}

func TestGeneratedPatchesAreWellFormedYAML(t *testing.T) {
	inputs := []Input{
		inputFor(templateWithLabels(map[string]string{"app": "web", "env": "prod"}), spread.StateAbsent, 3),
		inputFor(templateWithSpread(corev1.ScheduleAnyway, 5), spread.StateAdvisory, 3),
		inputFor(templateWithSpread(corev1.DoNotSchedule, 3), spread.StateEnforcedButWeak, 3),
		{
			Verdict: verdictWithSpread(spread.StateAbsent), Template: templateWithLabels(nil),
			Replicas: 1, DomainKey: zoneKey, Domains: []string{"zone-a", "zone-b", "zone-c"},
		},
		func() Input {
			in := inputFor(templateWithLabels(map[string]string{"app": "s"}), spread.StateAbsent, 1)
			in.PDB = pdbMinAvailable(1)
			return in
		}(),
		inputFor(templateWithPreferredZoneAntiAffinity(), spread.StateAbsent, 3),
	}
	for _, in := range inputs {
		for _, f := range Candidates(in) {
			var doc map[string]interface{}
			if err := yaml.Unmarshal([]byte(finalYAML(f.Patch)), &doc); err != nil {
				t.Fatalf("rung %d patch is not well-formed YAML: %v\n%s", f.Rung, err, f.Patch)
			}
		}
	}
}

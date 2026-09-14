package fix

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/SaiPisey2/kubectl-survive/internal/spread"
	"github.com/SaiPisey2/kubectl-survive/internal/volumepin"
)

// allScenarios covers every rung at least once.
func allScenarios() []Input {
	var out []Input

	// rungs 1 and 4: no spread, few replicas
	out = append(out, inputFor(templateWithLabels(map[string]string{"app": "web"}), spread.StateAbsent, 2))

	// rungs 2 and 3: an advisory, permissive constraint
	out = append(out, inputFor(templateWithSpread(corev1.ScheduleAnyway, 5), spread.StateAdvisory, 3))

	// rung 5: no PDB, enough replicas
	in5 := inputFor(templateWithLabels(map[string]string{"app": "web"}), spread.StateEnforced, 3)
	in5.PDB = nil
	out = append(out, in5)

	// rung 6: an unsatisfiable budget
	in6 := inputFor(templateWithLabels(map[string]string{"app": "web"}), spread.StateAbsent, 1)
	in6.PDB = pdbMinAvailable(1)
	out = append(out, in6)

	// rung 7: advisory anti-affinity
	out = append(out, inputFor(templateWithPreferredZoneAntiAffinity(), spread.StateAbsent, 3))

	// rung 8: a zonal volume pin
	in8 := inputFor(templateWithLabels(map[string]string{"app": "web"}), spread.StateEnforced, 3)
	in8.Verdict.VolumePins = []volumepin.Pin{{PVC: "data-0", PV: "pv-1", Domain: "zone-a"}}
	out = append(out, in8)

	return out
}

// These are cross-cutting invariants that no single rung owns, which is
// exactly why they are easy to lose: each rung's own tests pass while the set
// of them drifts. Both assertions here failed against earlier revisions of the
// ladder, so they are load-bearing rather than decorative.
func TestAdvRungSurvivabilityClaims(t *testing.T) {
	// Every rung must state its claim explicitly, and the PDB rungs and the
	// volume-pin finding must never claim to improve survivability.
	want := map[Rung]bool{
		RungSpreadAdd: true, RungSpreadEnforce: true, RungSpreadTighten: true,
		RungReplicasRaise: true, RungAntiAffinity: true,
		RungPDBAdd: false, RungPDBRepair: false, RungVolumePin: false,
	}
	seen := map[Rung]bool{}
	for _, in := range allScenarios() {
		for _, f := range Candidates(in) {
			seen[f.Rung] = true
			if got := f.ImprovesSurvivability; got != want[f.Rung] {
				t.Errorf("rung %d ImprovesSurvivability=%v, want %v", f.Rung, got, want[f.Rung])
			}
		}
	}
	for r := range want {
		if !seen[r] {
			t.Errorf("rung %d was never produced by any scenario; the check did not cover it", r)
		}
	}
}

func TestAdvNoRungEmitsAnEmptySelector(t *testing.T) {
	for _, in := range allScenarios() {
		in.Selector = nil
		in.Template = templateWithLabels(nil)
		for _, f := range Candidates(in) {
			if !f.Fixable() {
				continue
			}
			applied := f.Apply(templateWithLabels(nil))
			for _, c := range applied.Spec.TopologySpreadConstraints {
				if c.LabelSelector != nil && len(c.LabelSelector.MatchLabels) == 0 && len(c.LabelSelector.MatchExpressions) == 0 {
					t.Errorf("rung %d emitted an empty spread selector: matches every pod in the namespace", f.Rung)
				}
			}
			if f.Rung == RungPDBAdd {
				t.Errorf("rung 5 fired with no identifying labels: a namespace-wide PDB can hang every drain")
			}
		}
	}
}

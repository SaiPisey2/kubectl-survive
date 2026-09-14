package fix

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestApplyDoesNotTouchTheInput(t *testing.T) {
	// The caller's template describes the live cluster. A fix that mutated it
	// would corrupt every later comparison, including the ones proving it works.
	orig := templateWithLabels(map[string]string{"app": "web"})
	f := Fix{Rung: RungSpreadAdd, Mutate: func(t *corev1.PodTemplateSpec) {
		t.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{MaxSkew: 1}}
	}}
	got := f.Apply(orig)
	if len(orig.Spec.TopologySpreadConstraints) != 0 {
		t.Fatal("Apply mutated its input")
	}
	if len(got.Spec.TopologySpreadConstraints) != 1 {
		t.Fatal("Apply did not mutate its output")
	}
}

func TestArchitecturalFixesAreNotFixable(t *testing.T) {
	f := Fix{Rung: RungVolumePin, Architectural: "the volume is zonal"}
	if f.Fixable() {
		t.Fatal("a finding with no patch must never claim to be fixable")
	}
	if f.Mutate != nil {
		t.Fatal("an architectural finding must carry no mutation")
	}
}

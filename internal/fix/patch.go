// Package fix turns a survivability failure into a patch that is proven to
// schedule and proven to survive. Advice that has not been proven is worse than
// no advice: every survivability fix can make pods unschedulable, which is the
// outage the tool exists to prevent.
package fix

import corev1 "k8s.io/api/core/v1"

// Rung is a position on the ladder of spec §6.1. Rungs are evaluated in order
// and each is independently actionable.
type Rung int

const (
	RungSpreadAdd     Rung = 1 // add enforced topologySpreadConstraints
	RungSpreadEnforce Rung = 2 // ScheduleAnyway -> DoNotSchedule
	RungSpreadTighten Rung = 3 // lower maxSkew to 1
	RungReplicasRaise Rung = 4 // raise replicas to >= domain count
	RungPDBAdd        Rung = 5 // add a satisfiable budget
	RungPDBRepair     Rung = 6 // repair an unsatisfiable budget
	RungAntiAffinity  Rung = 7 // preferred -> required
	RungVolumePin     Rung = 8 // zonal volume: reported, never patched
)

// Fix is one rung's answer for one workload.
type Fix struct {
	Rung  Rung
	Title string // one line, imperative, no trailing punctuation

	// Mutate applies the change to a pod template. Nil for architectural
	// findings, which have no patch, and for rungs whose target field does not
	// live on the pod template (rung 4 raises replica count on the workload).
	Mutate func(*corev1.PodTemplateSpec)

	// Replicas is the target replica count when this rung changes it. Zero
	// means unchanged.
	Replicas int

	// Patch is the YAML fragment shown to the operator, as a diff body.
	Patch string

	// Architectural, when set, explains why no patch can solve this.
	Architectural string

	// ImprovesSurvivability reports whether applying this fix changes the
	// domain verdict (a pod stays up when the domain is lost), as opposed to
	// merely unblocking a drain. There is no default: every rung sets this
	// explicitly, because a fix whose claim is implicit is a fix nobody
	// checked. A PodDisruptionBudget rung (5, 6) never sets this true — a
	// budget is not consulted when a zone vanishes, so it answers "does a
	// drain hang", not "does this survive the zone dying". Rung 8 is a
	// reported finding, not a fix, and is false for the same reason.
	ImprovesSurvivability bool
}

// Fixable reports whether this rung produces a patch at all. Rung 4 has no
// template Mutate (the replica count lives on the workload, not the pod
// template) yet is still fixable; Architectural is the only thing that rules
// a rung out.
func (f Fix) Fixable() bool { return f.Architectural == "" }

// Apply returns a mutated copy, leaving the caller's template untouched.
func (f Fix) Apply(t *corev1.PodTemplateSpec) *corev1.PodTemplateSpec {
	out := t.DeepCopy()
	if f.Mutate != nil {
		f.Mutate(out)
	}
	return out
}

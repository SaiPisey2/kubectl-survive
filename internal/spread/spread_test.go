package spread

import (
	"testing"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func constraint(maxSkew int32, key string, when corev1.UnsatisfiableConstraintAction) corev1.TopologySpreadConstraint {
	return corev1.TopologySpreadConstraint{
		MaxSkew:           maxSkew,
		TopologyKey:       key,
		WhenUnsatisfiable: when,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name        string
		constraints []corev1.TopologySpreadConstraint
		replicas    int
		domains     int
		want        State
	}{
		{"no constraints", nil, 3, 3, StateAbsent},
		{
			"DoNotSchedule maxSkew 1 across 3 zones",
			[]corev1.TopologySpreadConstraint{constraint(1, domain.LabelZone, corev1.DoNotSchedule)},
			3, 3, StateEnforced,
		},
		{
			// maxSkew 3 with 3 replicas permits all 3 in one zone.
			"DoNotSchedule but maxSkew permits full concentration",
			[]corev1.TopologySpreadConstraint{constraint(3, domain.LabelZone, corev1.DoNotSchedule)},
			3, 3, StateEnforcedButWeak,
		},
		{
			"ScheduleAnyway is advisory only",
			[]corev1.TopologySpreadConstraint{constraint(1, domain.LabelZone, corev1.ScheduleAnyway)},
			3, 3, StateAdvisory,
		},
		{
			"constraint on another topology key does not protect zones",
			[]corev1.TopologySpreadConstraint{constraint(1, "kubernetes.io/hostname", corev1.DoNotSchedule)},
			3, 3, StateAbsent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.constraints, domain.LabelZone, tt.replicas, tt.domains)
			if got.State != tt.want {
				t.Errorf("Classify() = %v (%s), want %v", got.State, got.Detail, tt.want)
			}
		})
	}
}

func TestLiveSkew(t *testing.T) {
	tests := []struct {
		name      string
		placement map[string]int
		domains   []string
		want      int
	}{
		{"perfectly balanced", map[string]int{"a": 1, "b": 1, "c": 1}, []string{"a", "b", "c"}, 0},
		{"all in one zone", map[string]int{"a": 3}, []string{"a", "b", "c"}, 3},
		{"two of three", map[string]int{"a": 2, "b": 1}, []string{"a", "b", "c"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LiveSkew(tt.placement, tt.domains); got != tt.want {
				t.Errorf("LiveSkew() = %d, want %d", got, tt.want)
			}
		})
	}
}

// The scheduler ANDs constraints, so the tightest maxSkew binds regardless of
// the order they appear in the slice.
func TestClassifyTightestConstraintWins(t *testing.T) {
	tight := constraint(1, domain.LabelZone, corev1.DoNotSchedule)
	weak := constraint(5, domain.LabelZone, corev1.DoNotSchedule)

	for _, tc := range []struct {
		name        string
		constraints []corev1.TopologySpreadConstraint
	}{
		{"tight first", []corev1.TopologySpreadConstraint{tight, weak}},
		{"weak first", []corev1.TopologySpreadConstraint{weak, tight}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.constraints, domain.LabelZone, 3, 3)
			if got.State != StateEnforced {
				t.Errorf("Classify() = %v (%s), want %v", got.State, got.Detail, StateEnforced)
			}
		})
	}
}

// A constraint cannot guarantee spread when there is only one domain to spread across.
func TestClassifySingleDomainCannotEnforce(t *testing.T) {
	c := []corev1.TopologySpreadConstraint{constraint(1, domain.LabelZone, corev1.DoNotSchedule)}
	got := Classify(c, domain.LabelZone, 3, 1)
	if got.State != StateEnforcedButWeak {
		t.Errorf("Classify() with 1 domain = %v (%s), want %v", got.State, got.Detail, StateEnforcedButWeak)
	}
}

// minDomains is the only field that actually forces spread: it requires the
// scheduler to use at least that many domains even when maxSkew alone would
// permit concentrating all replicas in one.
func TestClassifyMinDomainsForcesSpread(t *testing.T) {
	minDomains := int32(3)
	c := constraint(3, domain.LabelZone, corev1.DoNotSchedule)
	c.MinDomains = &minDomains

	got := Classify([]corev1.TopologySpreadConstraint{c}, domain.LabelZone, 3, 3)
	if got.State != StateEnforced {
		t.Errorf("Classify() with minDomains=3 = %v (%s), want %v", got.State, got.Detail, StateEnforced)
	}

	// The same constraint without minDomains is weak: maxSkew 3 with 3
	// replicas permits every replica in one domain.
	without := constraint(3, domain.LabelZone, corev1.DoNotSchedule)
	got = Classify([]corev1.TopologySpreadConstraint{without}, domain.LabelZone, 3, 3)
	if got.State != StateEnforcedButWeak {
		t.Errorf("Classify() without minDomains = %v (%s), want %v", got.State, got.Detail, StateEnforcedButWeak)
	}
}

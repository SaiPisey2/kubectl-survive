package spread

import (
	"testing"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func term(key string) corev1.PodAffinityTerm {
	return corev1.PodAffinityTerm{
		TopologyKey:   key,
		LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
	}
}

func TestClassifyAntiAffinity(t *testing.T) {
	required := &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{term(domain.LabelZone)},
	}}
	preferred := &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
			{Weight: 100, PodAffinityTerm: term(domain.LabelZone)},
		},
	}}
	otherKey := &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{term("kubernetes.io/hostname")},
	}}

	tests := []struct {
		name     string
		affinity *corev1.Affinity
		want     State
	}{
		{"nil affinity", nil, StateAbsent},
		{"required on zone", required, StateEnforced},
		{"preferred on zone", preferred, StateAdvisory},
		{"required on another key", otherKey, StateAbsent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyAntiAffinity(tt.affinity, domain.LabelZone); got.State != tt.want {
				t.Errorf("ClassifyAntiAffinity() = %v, want %v", got.State, tt.want)
			}
		})
	}
}

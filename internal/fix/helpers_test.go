package fix

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// templateWithLabels builds a minimal pod template carrying the given pod
// labels and no topology spread constraints. Later tasks add more helpers
// here as the ladder grows more rungs to exercise.
func templateWithLabels(labels map[string]string) *corev1.PodTemplateSpec {
	return &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "example/app:latest"}},
		},
	}
}

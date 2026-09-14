package health

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pod(phase corev1.PodPhase, ready, deleting bool) *corev1.Pod {
	p := &corev1.Pod{Status: corev1.PodStatus{Phase: phase}}
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: cond}}
	if deleting {
		now := metav1.NewTime(time.Now())
		p.DeletionTimestamp = &now
	}
	return p
}

func TestAvailable(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{"running and ready", pod(corev1.PodRunning, true, false), true},
		{"running not ready", pod(corev1.PodRunning, false, false), false},
		// Upstream #123911: the disruption controller counts terminating pods
		// as healthy. We must not.
		{"terminating but still ready", pod(corev1.PodRunning, true, true), false},
		{"pending", pod(corev1.PodPending, false, false), false},
		{"succeeded", pod(corev1.PodSucceeded, false, false), false},
		{"failed", pod(corev1.PodFailed, false, false), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Available(tt.pod); got != tt.want {
				t.Errorf("Available() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIgnoredByPDB(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{"pending bypasses PDB", pod(corev1.PodPending, false, false), true},
		{"succeeded bypasses PDB", pod(corev1.PodSucceeded, false, false), true},
		{"failed bypasses PDB", pod(corev1.PodFailed, false, false), true},
		{"already deleting bypasses PDB", pod(corev1.PodRunning, true, true), true},
		{"running healthy is protected", pod(corev1.PodRunning, true, false), false},
		{"running unready is still evaluated", pod(corev1.PodRunning, false, false), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IgnoredByPDB(tt.pod); got != tt.want {
				t.Errorf("IgnoredByPDB() = %v, want %v", got, tt.want)
			}
		})
	}
}

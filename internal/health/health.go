// Package health defines what "an available pod" means for survivability.
//
// This is deliberately the only place that decision is made. The disruption
// controller's status.currentHealthy is NOT usable for our maths: upstream
// issue #123911 (open) counts Terminating pods as healthy, which is exactly
// the case that breaks a rolling restart's availability guarantee.
package health

import corev1 "k8s.io/api/core/v1"

// Available reports whether a pod is currently serving: Running, Ready, and
// not being deleted.
func Available(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// IgnoredByPDB reports whether the eviction API skips PDB checks for this pod
// entirely, mirroring canIgnorePDB() in the API server: Pending, Succeeded,
// Failed and already-terminating pods can always be evicted.
func IgnoredByPDB(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return true
	}
	switch pod.Status.Phase {
	case corev1.PodPending, corev1.PodSucceeded, corev1.PodFailed:
		return true
	}
	return false
}

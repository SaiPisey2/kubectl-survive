// internal/snapshot/types.go
package snapshot

import (
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	policyv1 "k8s.io/api/policy/v1"
)

// Snapshot is the engine's only input. Everything downstream is a pure
// function of this struct, which is what makes the engine testable without a
// cluster and reproducible from a dump.
type Snapshot struct {
	TakenAt time.Time

	Nodes          []*corev1.Node
	Pods           []*corev1.Pod
	PDBs           []*policyv1.PodDisruptionBudget
	PVCs           []*corev1.PersistentVolumeClaim
	PVs            []*corev1.PersistentVolume
	ReplicaSets    []*appsv1.ReplicaSet
	Deployments    []*appsv1.Deployment
	StatefulSets   []*appsv1.StatefulSet
	Services       []*corev1.Service
	EndpointSlices []*discoveryv1.EndpointSlice
}

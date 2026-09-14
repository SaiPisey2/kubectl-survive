package workload

import (
	"testing"

	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func i32(i int32) *int32 { return &i }
func bp(b bool) *bool    { return &b }

func TestOwnerWalksPodToReplicaSetToDeployment(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "default", UID: "dep-uid"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(3)},
	}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "checkout-abc", Namespace: "default", UID: "rs-uid",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "checkout", UID: "dep-uid", Controller: bp(true)}},
	}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "checkout-abc-1", Namespace: "default",
		OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "checkout-abc", UID: "rs-uid", Controller: bp(true)}},
	}}

	idx := NewIndex(&snapshot.Snapshot{
		Pods:        []*corev1.Pod{pod},
		ReplicaSets: []*appsv1.ReplicaSet{rs},
		Deployments: []*appsv1.Deployment{dep},
	})

	ref, ok := idx.Owner(pod)
	if !ok {
		t.Fatal("Owner() not found")
	}
	want := Ref{Kind: "Deployment", Namespace: "default", Name: "checkout"}
	if ref != want {
		t.Errorf("Owner() = %+v, want %+v", ref, want)
	}
	if n, ok := idx.DesiredReplicas(ref); !ok || n != 3 {
		t.Errorf("DesiredReplicas() = (%d,%v), want (3,true)", n, ok)
	}
}

func TestOwnerHandlesBarePod(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "solo", Namespace: "default"}}
	idx := NewIndex(&snapshot.Snapshot{Pods: []*corev1.Pod{pod}})
	if _, ok := idx.Owner(pod); ok {
		t.Error("Owner() reported an owner for an unmanaged pod")
	}
}

// Package workload maps pods to the thing an operator actually names.
package workload

import (
	"fmt"

	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type Ref struct {
	Kind      string
	Namespace string
	Name      string
}

func (r Ref) String() string { return fmt.Sprintf("%s/%s", r.Namespace, r.Name) }

type Index struct {
	rsByUID  map[types.UID]*appsv1.ReplicaSet
	replicas map[Ref]int32
}

func NewIndex(s *snapshot.Snapshot) *Index {
	i := &Index{
		rsByUID:  map[types.UID]*appsv1.ReplicaSet{},
		replicas: map[Ref]int32{},
	}
	for _, rs := range s.ReplicaSets {
		i.rsByUID[rs.UID] = rs
	}
	for _, d := range s.Deployments {
		if d.Spec.Replicas != nil {
			i.replicas[Ref{"Deployment", d.Namespace, d.Name}] = *d.Spec.Replicas
		}
	}
	for _, st := range s.StatefulSets {
		if st.Spec.Replicas != nil {
			i.replicas[Ref{"StatefulSet", st.Namespace, st.Name}] = *st.Spec.Replicas
		}
	}
	return i
}

func controllerOf(obj metav1.Object) *metav1.OwnerReference {
	refs := obj.GetOwnerReferences()
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

// Owner resolves a pod to the workload an operator would name, walking
// Pod -> ReplicaSet -> Deployment where that chain exists.
func (i *Index) Owner(pod *corev1.Pod) (Ref, bool) {
	ctrl := controllerOf(pod)
	if ctrl == nil {
		return Ref{}, false
	}
	if ctrl.Kind != "ReplicaSet" {
		return Ref{Kind: ctrl.Kind, Namespace: pod.Namespace, Name: ctrl.Name}, true
	}
	rs, ok := i.rsByUID[ctrl.UID]
	if !ok {
		return Ref{Kind: "ReplicaSet", Namespace: pod.Namespace, Name: ctrl.Name}, true
	}
	if up := controllerOf(rs); up != nil && up.Kind == "Deployment" {
		return Ref{Kind: "Deployment", Namespace: rs.Namespace, Name: up.Name}, true
	}
	return Ref{Kind: "ReplicaSet", Namespace: rs.Namespace, Name: rs.Name}, true
}

// DesiredReplicas returns the workload's declared replica count.
func (i *Index) DesiredReplicas(r Ref) (int32, bool) {
	n, ok := i.replicas[r]
	return n, ok
}

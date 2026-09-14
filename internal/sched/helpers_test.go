package sched

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
)

// node builds a ready, schedulable node in the given zone with enough
// capacity that NodeResourcesFit never rejects it on its own.
func node(name, zone string) *corev1.Node {
	capacity := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("8"),
		corev1.ResourceMemory: resource.MustParse("32Gi"),
		corev1.ResourcePods:   resource.MustParse("110"),
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  types.UID(name),
			Labels: map[string]string{
				domain.LabelZone: zone,
				// Hostname topology is what pod anti-affinity is usually written
				// against. A node missing this label is silently skipped by the
				// spread and affinity plugins, so an omission here would make
				// later tests pass or fail for reasons unrelated to what they
				// are testing.
				corev1.LabelHostname: name,
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: capacity,
			Capacity:    capacity,
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
}

// assignedPod builds a pod bound to nodeName, with a distinct UID and a
// non-empty namespace, ready to seed the scheduler cache.
func assignedPod(name, nodeName string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID(name),
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "example.test/app:latest",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
}

// unassignedPod builds a pod with no NodeName, ready to be evaluated as a
// scheduling candidate against the framework's Filter plugins.
func unassignedPod(name string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID(name),
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "example.test/app:latest",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				},
			},
		},
	}
}

// snapshotWith constructs a snapshot.Snapshot directly from nodes and pods,
// which is all New consumes.
func snapshotWith(nodes []*corev1.Node, pods []*corev1.Pod) *snapshot.Snapshot {
	return &snapshot.Snapshot{
		TakenAt: time.Now(),
		Nodes:   nodes,
		Pods:    pods,
	}
}

// mustNew calls New and fails the test immediately on error, for tests whose
// focus is downstream of a working framework.
func mustNew(t *testing.T, nodes []*corev1.Node, pods []*corev1.Pod) *Scheduler {
	t.Helper()
	s, err := New(context.Background(), snapshotWith(nodes, pods))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

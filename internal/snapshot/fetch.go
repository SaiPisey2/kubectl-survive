// internal/snapshot/fetch.go
package snapshot

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Fetch reads everything the engine needs in one pass.
//
// Any failure is fatal by design. Omitting a node or a PDB does not degrade
// the answer, it inverts it: a workload whose only other replica lives on a
// node we could not read would be reported as surviving.
func Fetch(ctx context.Context, cs kubernetes.Interface) (*Snapshot, error) {
	s := &Snapshot{TakenAt: time.Now().UTC()}
	all := metav1.ListOptions{}

	nodes, err := cs.CoreV1().Nodes().List(ctx, all)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	for i := range nodes.Items {
		s.Nodes = append(s.Nodes, &nodes.Items[i])
	}

	pods, err := cs.CoreV1().Pods(metav1.NamespaceAll).List(ctx, all)
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	for i := range pods.Items {
		s.Pods = append(s.Pods, &pods.Items[i])
	}

	pdbs, err := cs.PolicyV1().PodDisruptionBudgets(metav1.NamespaceAll).List(ctx, all)
	if err != nil {
		return nil, fmt.Errorf("list poddisruptionbudgets: %w", err)
	}
	for i := range pdbs.Items {
		s.PDBs = append(s.PDBs, &pdbs.Items[i])
	}

	pvcs, err := cs.CoreV1().PersistentVolumeClaims(metav1.NamespaceAll).List(ctx, all)
	if err != nil {
		return nil, fmt.Errorf("list persistentvolumeclaims: %w", err)
	}
	for i := range pvcs.Items {
		s.PVCs = append(s.PVCs, &pvcs.Items[i])
	}

	pvs, err := cs.CoreV1().PersistentVolumes().List(ctx, all)
	if err != nil {
		return nil, fmt.Errorf("list persistentvolumes: %w", err)
	}
	for i := range pvs.Items {
		s.PVs = append(s.PVs, &pvs.Items[i])
	}

	rs, err := cs.AppsV1().ReplicaSets(metav1.NamespaceAll).List(ctx, all)
	if err != nil {
		return nil, fmt.Errorf("list replicasets: %w", err)
	}
	for i := range rs.Items {
		s.ReplicaSets = append(s.ReplicaSets, &rs.Items[i])
	}

	deps, err := cs.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, all)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	for i := range deps.Items {
		s.Deployments = append(s.Deployments, &deps.Items[i])
	}

	sts, err := cs.AppsV1().StatefulSets(metav1.NamespaceAll).List(ctx, all)
	if err != nil {
		return nil, fmt.Errorf("list statefulsets: %w", err)
	}
	for i := range sts.Items {
		s.StatefulSets = append(s.StatefulSets, &sts.Items[i])
	}

	// Services and EndpointSlices back the dependency graph (spec §5.5):
	// EndpointSlices are the truth about who actually serves a Service, and
	// Services are needed to know a referenced DNS name is real rather than
	// an unrelated env var value that happens to look like one.
	svcs, err := cs.CoreV1().Services(metav1.NamespaceAll).List(ctx, all)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	for i := range svcs.Items {
		s.Services = append(s.Services, &svcs.Items[i])
	}

	slices, err := cs.DiscoveryV1().EndpointSlices(metav1.NamespaceAll).List(ctx, all)
	if err != nil {
		return nil, fmt.Errorf("list endpointslices: %w", err)
	}
	for i := range slices.Items {
		s.EndpointSlices = append(s.EndpointSlices, &slices.Items[i])
	}

	return s, nil
}

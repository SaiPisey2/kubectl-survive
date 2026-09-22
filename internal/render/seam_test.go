package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func i32(i int32) *int32 { return &i }
func bp(b bool) *bool    { return &b }

func seamTestNode(name, zone string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: name, Labels: map[string]string{domain.LabelZone: zone},
	}}
}

// seamDeployWith mirrors internal/survive's deployWith test helper: it
// builds a Deployment, its ReplicaSet, and Running/Ready pods placed on
// nodes, so this file can drive the real survive.Analyze entry point
// without a hand-built survive.Report or depgraph.Graph.
func seamDeployWith(name string, replicas int32, nodes []string) (*appsv1.Deployment, *appsv1.ReplicaSet, []*corev1.Pod) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name + "-dep")},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(replicas)},
	}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: name + "-rs", Namespace: "default", UID: types.UID(name + "-rs"),
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: name, UID: dep.UID, Controller: bp(true)}},
	}}
	var pods []*corev1.Pod
	for i, n := range nodes {
		pods = append(pods, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name + "-" + string(rune('a'+i)), Namespace: "default",
				Labels:          map[string]string{"app": name},
				OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: bp(true)}},
			},
			Spec: corev1.PodSpec{NodeName: n},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			},
		})
	}
	return dep, rs, pods
}

// TestExternalNameDependencyRendersNoImpairedRows is the seam test the
// coordinator asked for: an ExternalName Service dependency (the RDS/Cloud
// SQL pattern), driven through the real survive.Analyze entry point and the
// real render.Table, confirming the rendered table has zero "IMPAIRED"
// rows. Ruling: an ExternalName Service points outside the cluster and can
// never resolve to a workload, so it must never be reported as impairing --
// a false edge is worse than a missing one.
func TestExternalNameDependencyRendersNoImpairedRows(t *testing.T) {
	webDep, webRS, webPods := seamDeployWith("web", 2, []string{"n1a", "n1b"})
	webPods[0].Spec.Containers = []corev1.Container{{
		Name: "web",
		Env: []corev1.EnvVar{
			{Name: "DB_ADDR", Value: "postgres://db:5432/app"},
		},
	}}
	webPods[1].Spec.Containers = webPods[0].Spec.Containers

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Type:         corev1.ServiceTypeExternalName,
			ExternalName: "prod.abc123.us-east-1.rds.amazonaws.com",
		},
	}

	s := &snapshot.Snapshot{
		Nodes:       []*corev1.Node{seamTestNode("n1a", "us-east-1a"), seamTestNode("n1b", "us-east-1b")},
		Pods:        webPods,
		ReplicaSets: []*appsv1.ReplicaSet{webRS},
		Deployments: []*appsv1.Deployment{webDep},
		Services:    []*corev1.Service{svc},
	}

	r := survive.Analyze(s, domain.LabelZone)

	var buf bytes.Buffer
	if err := Table(&buf, r); err != nil {
		t.Fatalf("Table() error = %v", err)
	}
	if strings.Contains(buf.String(), "IMPAIRED") {
		t.Errorf("rendered table contains an IMPAIRED row for an ExternalName dependency:\n%s", buf.String())
	}
}

package exporter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/sched"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
)

func i32(i int32) *int32 { return &i }
func bp(b bool) *bool    { return &b }

// node builds a ready, schedulable node in the given zone with enough
// capacity that NodeResourcesFit never rejects it on its own -- copied from
// internal/sched/helpers_test.go and internal/draincheck/draincheck_test.go
// so this package's tests need neither a cluster nor those packages' internal
// (unexported) test helpers.
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
				domain.LabelZone:     zone,
				corev1.LabelHostname: name,
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: capacity,
			Capacity:    capacity,
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

// deployWith builds a Deployment, its ReplicaSet, and pods placed on nodes.
func deployWith(name string, replicas int32, nodes []string) (*appsv1.Deployment, *appsv1.ReplicaSet, []*corev1.Pod) {
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
				Name:            fmt.Sprintf("%s-%d", name, i),
				Namespace:       "default",
				Labels:          map[string]string{"app": name},
				OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: bp(true)}},
			},
			Spec: corev1.PodSpec{
				NodeName:   n,
				Containers: []corev1.Container{{Name: "app", Image: "example.test/app:latest"}},
			},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			},
		})
	}
	return dep, rs, pods
}

// scrape starts a real HTTP server over e's handler, fetches /metrics, and
// returns the body. A real listener (loopback only) is used deliberately --
// it is what proves the handler is actually wired up, not just that the
// Collector code compiles.
func scrape(t *testing.T, e *Exporter) string {
	t.Helper()
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", resp.StatusCode, body)
	}
	return string(body)
}

func mustContainLine(t *testing.T, body, want string) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == want {
			return
		}
	}
	t.Fatalf("metrics output does not contain line %q; got:\n%s", want, body)
}

func mustNotContain(t *testing.T, body, want string) {
	t.Helper()
	if strings.Contains(body, want) {
		t.Fatalf("metrics output must not contain %q; got:\n%s", want, body)
	}
}

// TestDomainAndWorkloadMetrics is the core contract: a workload lost in one
// zone and surviving in another must render as the exact series spec 8.4
// specifies, with the version-gated drain-deadlock check correctly reported
// as not having run.
func TestDomainAndWorkloadMetrics(t *testing.T) {
	_, rs, pods := deployWith("checkout-api", 3, []string{"n1a", "n1a", "n1a"})
	snap := &snapshot.Snapshot{
		TakenAt:     time.Now(),
		Nodes:       []*corev1.Node{node("n1a", "us-east-1a"), node("n1b", "us-east-1b")},
		Pods:        pods,
		ReplicaSets: []*appsv1.ReplicaSet{rs},
		Deployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "checkout-api", Namespace: "default", UID: rs.OwnerReferences[0].UID}, Spec: appsv1.DeploymentSpec{Replicas: i32(3)}}},
	}

	e := New(domain.LabelZone)
	fetch := func(ctx context.Context) (*snapshot.Snapshot, sched.VersionGate, error) {
		return snap, sched.VersionGate{Enabled: false, Warning: "gate disabled for this test"}, nil
	}
	if err := e.runOnce(context.Background(), fetch, time.Second); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	body := scrape(t, e)

	mustContainLine(t, body, `survive_workloads_lost{domain="us-east-1a"} 1`)
	mustContainLine(t, body, `survive_workloads_degraded{domain="us-east-1a"} 0`)
	mustContainLine(t, body, `survive_workloads_lost{domain="us-east-1b"} 0`)
	mustContainLine(t, body, `survive_workload_survives{domain="us-east-1a",workload="default/checkout-api"} 0`)
	mustContainLine(t, body, `survive_workload_survives{domain="us-east-1b",workload="default/checkout-api"} 1`)

	// The gate was disabled, so the scheduler-backed check must be reported
	// as not having run -- distinct from having run and found nothing.
	mustContainLine(t, body, "survive_drain_deadlock_check_enabled 0")

	mustContainLine(t, body, "survive_scrape_success 1")
}

func TestPDBUnsatisfiableMetric(t *testing.T) {
	two := intstr.FromInt32(2)
	_, rs, pods := deployWith("session-store", 2, []string{"n1a", "n1b"})
	snap := &snapshot.Snapshot{
		TakenAt:     time.Now(),
		Nodes:       []*corev1.Node{node("n1a", "us-east-1a"), node("n1b", "us-east-1b")},
		Pods:        pods,
		ReplicaSets: []*appsv1.ReplicaSet{rs},
		PDBs: []*policyv1.PodDisruptionBudget{{
			ObjectMeta: metav1.ObjectMeta{Name: "session-store", Namespace: "default", Generation: 1},
			Spec: policyv1.PodDisruptionBudgetSpec{
				MinAvailable: &two,
				Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "session-store"}},
			},
			Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1},
		}},
	}

	e := New(domain.LabelZone)
	fetch := func(ctx context.Context) (*snapshot.Snapshot, sched.VersionGate, error) {
		return snap, sched.VersionGate{Enabled: false}, nil
	}
	if err := e.runOnce(context.Background(), fetch, time.Second); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	body := scrape(t, e)
	mustContainLine(t, body, `survive_pdb_unsatisfiable{pdb="default/session-store"} 1`)
}

// TestDrainDeadlockMetric exercises the gate-enabled path end to end,
// including the real scheduler (in-memory, via a fake clientset -- no
// cluster or network involved, same as internal/draincheck's own tests).
func TestDrainDeadlockMetric(t *testing.T) {
	nodes := []*corev1.Node{node("n-a", "zone-a"), node("n-b", "zone-b"), node("n-c", "zone-c")}
	labels := map[string]string{"app": "web"}
	controller := true
	one := intstr.FromInt32(1)

	pod := func(name, nodeName string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "default", UID: types.UID(name), Labels: labels,
				OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web", UID: types.UID("web"), Controller: &controller}},
			},
			Spec: corev1.PodSpec{
				NodeName:   nodeName,
				Containers: []corev1.Container{{Name: "app", Image: "example.test/app:latest"}},
				TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
					MaxSkew: 1, TopologyKey: domain.LabelZone, WhenUnsatisfiable: corev1.DoNotSchedule,
					LabelSelector: &metav1.LabelSelector{MatchLabels: labels},
				}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
		}
	}
	pods := []*corev1.Pod{pod("web-a", "n-a"), pod("web-b", "n-b"), pod("web-c", "n-c")}

	snap := &snapshot.Snapshot{
		TakenAt: time.Now(),
		Nodes:   nodes,
		Pods:    pods,
		PDBs: []*policyv1.PodDisruptionBudget{{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default", Generation: 1},
			Spec: policyv1.PodDisruptionBudgetSpec{
				MaxUnavailable: &one,
				Selector:       &metav1.LabelSelector{MatchLabels: labels},
			},
			Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1},
		}},
	}

	e := New(domain.LabelZone)
	fetch := func(ctx context.Context) (*snapshot.Snapshot, sched.VersionGate, error) {
		return snap, sched.VersionGate{Enabled: true}, nil
	}
	if err := e.runOnce(context.Background(), fetch, 30*time.Second); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	body := scrape(t, e)
	mustContainLine(t, body, "survive_drain_deadlock_check_enabled 1")
	if !strings.Contains(body, `survive_drain_deadlock{`) {
		t.Fatalf("expected a survive_drain_deadlock series, got:\n%s", body)
	}
	mustContainLine(t, body, "survive_scrape_success 1")
}

// TestFailedFetchMarksScrapeUnhealthyWithoutClobberingLastGood is the
// silent-failure-mode guard the spec calls out explicitly: a failed analysis
// must be distinguishable from a healthy cluster with nothing wrong, and it
// must not overwrite the last good reading with misleading zeros.
func TestFailedFetchMarksScrapeUnhealthyWithoutClobberingLastGood(t *testing.T) {
	_, rs, pods := deployWith("checkout-api", 3, []string{"n1a", "n1a", "n1a"})
	snap := &snapshot.Snapshot{
		TakenAt:     time.Now(),
		Nodes:       []*corev1.Node{node("n1a", "us-east-1a"), node("n1b", "us-east-1b")},
		Pods:        pods,
		ReplicaSets: []*appsv1.ReplicaSet{rs},
	}

	e := New(domain.LabelZone)
	good := func(ctx context.Context) (*snapshot.Snapshot, sched.VersionGate, error) {
		return snap, sched.VersionGate{Enabled: false}, nil
	}
	if err := e.runOnce(context.Background(), good, time.Second); err != nil {
		t.Fatalf("runOnce (good): %v", err)
	}
	bodyBefore := scrape(t, e)
	mustContainLine(t, bodyBefore, `survive_workloads_lost{domain="us-east-1a"} 1`)
	mustContainLine(t, bodyBefore, "survive_scrape_success 1")

	failing := func(ctx context.Context) (*snapshot.Snapshot, sched.VersionGate, error) {
		return nil, sched.VersionGate{}, errors.New("snapshot.Fetch: connection refused")
	}
	if err := e.runOnce(context.Background(), failing, time.Second); err == nil {
		t.Fatal("runOnce (failing) returned nil error, want the fetch error surfaced")
	}

	bodyAfter := scrape(t, e)
	// The last known-good reading must still be there...
	mustContainLine(t, bodyAfter, `survive_workloads_lost{domain="us-east-1a"} 1`)
	// ...but the scrape is now flagged unhealthy, and the error counter moved.
	mustContainLine(t, bodyAfter, "survive_scrape_success 0")
	mustContainLine(t, bodyAfter, "survive_scrape_errors_total 1")
}

// TestUnlabelledNodesMetric checks the cheap, always-cardinality-1 signal for
// nodes the domain key cannot place -- the same fact internal/survive.Report
// carries, just made scrapeable.
func TestUnlabelledNodesMetric(t *testing.T) {
	snap := &snapshot.Snapshot{
		TakenAt: time.Now(),
		Nodes: []*corev1.Node{
			node("n1a", "us-east-1a"),
			{ObjectMeta: metav1.ObjectMeta{Name: "mystery"}},
		},
	}
	e := New(domain.LabelZone)
	fetch := func(ctx context.Context) (*snapshot.Snapshot, sched.VersionGate, error) {
		return snap, sched.VersionGate{Enabled: false}, nil
	}
	if err := e.runOnce(context.Background(), fetch, time.Second); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	body := scrape(t, e)
	mustContainLine(t, body, "survive_unlabelled_nodes 1")
}

// TestGaugesDoNotAccumulateDeletedWorkloads: cardinality must track the
// cluster's current shape, not its history. A workload that disappears
// between cycles (scaled to zero and deleted, say) must not leave a zombie
// series behind forever.
func TestGaugesDoNotAccumulateDeletedWorkloads(t *testing.T) {
	_, rs1, pods1 := deployWith("will-vanish", 1, []string{"n1a"})
	nodes := []*corev1.Node{node("n1a", "us-east-1a"), node("n1b", "us-east-1b")}
	snap1 := &snapshot.Snapshot{TakenAt: time.Now(), Nodes: nodes, Pods: pods1, ReplicaSets: []*appsv1.ReplicaSet{rs1}}

	e := New(domain.LabelZone)
	fetch1 := func(ctx context.Context) (*snapshot.Snapshot, sched.VersionGate, error) {
		return snap1, sched.VersionGate{Enabled: false}, nil
	}
	if err := e.runOnce(context.Background(), fetch1, time.Second); err != nil {
		t.Fatalf("runOnce 1: %v", err)
	}
	mustContainLine(t, scrape(t, e), `survive_workload_survives{domain="us-east-1a",workload="default/will-vanish"} 0`)

	snap2 := &snapshot.Snapshot{TakenAt: time.Now(), Nodes: nodes}
	fetch2 := func(ctx context.Context) (*snapshot.Snapshot, sched.VersionGate, error) {
		return snap2, sched.VersionGate{Enabled: false}, nil
	}
	if err := e.runOnce(context.Background(), fetch2, time.Second); err != nil {
		t.Fatalf("runOnce 2: %v", err)
	}
	mustNotContain(t, scrape(t, e), "will-vanish")
}

// TestRunLoopDoesNotBlockScrapesOnSlowAnalysis is the concurrency contract
// spelled out in the milestone: analysis must not run on the scrape
// goroutine, so a slow analysis pass must never stall a concurrent scrape.
func TestRunLoopDoesNotBlockScrapesOnSlowAnalysis(t *testing.T) {
	_, rs, pods := deployWith("api", 3, []string{"n1a", "n1b", "n1a"})
	snap := &snapshot.Snapshot{
		TakenAt:     time.Now(),
		Nodes:       []*corev1.Node{node("n1a", "us-east-1a"), node("n1b", "us-east-1b")},
		Pods:        pods,
		ReplicaSets: []*appsv1.ReplicaSet{rs},
	}

	e := New(domain.LabelZone)
	var calls int32
	slow := func(ctx context.Context) (*snapshot.Snapshot, sched.VersionGate, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(150 * time.Millisecond)
		return snap, sched.VersionGate{Enabled: false}, nil
	}

	srv := httptest.NewServer(e.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, slow, 50*time.Millisecond, time.Second) }()

	// The very first analysis pass is already sleeping in the background by
	// the time this fires (best-effort short delay), yet the scrape must
	// still complete promptly -- it talks to promhttp, never to the loop.
	time.Sleep(20 * time.Millisecond)
	scrapeStart := time.Now()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET during slow analysis: %v", err)
	}
	resp.Body.Close()
	if elapsed := time.Since(scrapeStart); elapsed > 100*time.Millisecond {
		t.Fatalf("scrape took %s while analysis was sleeping; it must not block on the analysis loop", elapsed)
	}

	<-ctx.Done()
	<-done
	if atomic.LoadInt32(&calls) == 0 {
		t.Fatal("background loop never invoked fetch")
	}
}

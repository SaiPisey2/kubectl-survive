//go:build harness

package harness

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
)

// serviceManifest renders a Service selecting app=name on port 80, and
// backendDeployment renders a Deployment whose pods carry that label -- the
// pair a real endpointslice controller resolves into EndpointSlice endpoints.
func serviceManifest(name string) string {
	return fmt.Sprintf(`
apiVersion: v1
kind: Service
metadata: {name: %s}
spec:
  selector: {app: %s}
  ports: [{port: 80, targetPort: 80}]
`, name, name)
}

// pinnedDeployment renders a single-replica Deployment forced onto a named
// node via nodeName, so its zone is deterministic for the test. When target
// is non-empty, the container also carries a literal env var naming it by
// DNS in host position (spec §5.5's conservative matching rule) -- the only
// kind of edge internal/depgraph ever follows.
func pinnedDeployment(name, node, target string) string {
	env := ""
	if target != "" {
		env = fmt.Sprintf("        env: [{name: BACKEND_ADDR, value: \"http://%s:80\"}]\n", target)
	}
	return fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata: {name: %s}
spec:
  replicas: 1
  selector: {matchLabels: {app: %s}}
  template:
    metadata: {labels: {app: %s}}
    spec:
      nodeName: %s
      tolerations: [{key: kwok.x-k8s.io/node, operator: Exists, effect: NoSchedule}]
      containers:
      - name: c
        image: nginx
%s        resources: {requests: {cpu: 100m, memory: 128Mi}}
`, name, name, name, node, env)
}

// TestDepgraphResolvesRealEndpointSlice proves internal/depgraph's edges
// resolve from EndpointSlice endpoints a real endpointslice controller
// actually produced under KWOK -- not from a hand-built snapshot.Snapshot,
// which every unit test in internal/depgraph uses and which previously let a
// wiring defect between the real controller's output and Build ship
// unnoticed. frontend depends on backend through a literal env var naming
// backend's Service in host position; backend has its only replica in
// us-east-1a, so losing that zone must report frontend impaired there, and
// only there.
func TestDepgraphResolvesRealEndpointSlice(t *testing.T) {
	c := NewCluster(t, "survive-depgraph")
	ns := "depgraph-test"
	c.ApplyNamespace(t, ns)
	t.Cleanup(func() { c.DeleteNamespace(t, ns) })

	c.AddNode(t, "dg-n1a", "us-east-1a")
	c.AddNode(t, "dg-n1b", "us-east-1b")
	c.WaitNodeSchedulable(t, "dg-n1a", 60*time.Second)
	c.WaitNodeSchedulable(t, "dg-n1b", 60*time.Second)

	applyIn(t, ns, serviceManifest("backend"))
	applyIn(t, ns, pinnedDeployment("backend", "dg-n1a", ""))
	applyIn(t, ns, pinnedDeployment("frontend", "dg-n1b", "backend"))
	c.WaitPodsReady(t, ns, 2, 90*time.Second)

	// The endpointslice controller only publishes endpoints once it has
	// observed the backend pod's readiness; snapshot.Fetch must not race it.
	waitFor(t, 60*time.Second, "an EndpointSlice with a resolvable backer for backend", func() bool {
		slices, err := c.Client.DiscoveryV1().EndpointSlices(ns).List(context.Background(), metav1.ListOptions{
			LabelSelector: discoveryv1.LabelServiceName + "=backend",
		})
		if err != nil {
			return false
		}
		for i := range slices.Items {
			for _, ep := range slices.Items[i].Endpoints {
				if ep.TargetRef != nil && ep.TargetRef.Kind == "Pod" {
					return true
				}
			}
		}
		return false
	})

	snap, err := snapshot.Fetch(context.Background(), c.Client)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	report := survive.Analyze(snap, domain.LabelZone)

	var dr *survive.DomainResult
	for i := range report.Domains {
		if report.Domains[i].Domain == "us-east-1a" {
			dr = &report.Domains[i]
		}
	}
	if dr == nil {
		t.Fatal("no DomainResult for us-east-1a")
	}

	var frontendVerdict *survive.Verdict
	for i := range dr.Verdicts {
		if dr.Verdicts[i].Workload.Namespace == ns && dr.Verdicts[i].Workload.Name == "frontend" {
			frontendVerdict = &dr.Verdicts[i]
		}
	}
	if frontendVerdict == nil {
		t.Fatal("no verdict for frontend in us-east-1a")
	}
	// frontend's own pod is in us-east-1b, so its own outcome is unaffected
	// by losing us-east-1a: ruling 1, the dependency layer never changes it.
	if frontendVerdict.Outcome != survive.OutcomeSurvives {
		t.Errorf("frontend outcome in us-east-1a = %v, want OutcomeSurvives (own pod is in us-east-1b)", frontendVerdict.Outcome)
	}
	if len(frontendVerdict.DependsOn) != 1 {
		t.Fatalf("frontend.DependsOn = %v, want exactly one dependency resolved from the real EndpointSlice", frontendVerdict.DependsOn)
	}

	found := false
	for _, imp := range dr.Impairments {
		if imp.Workload.Namespace == ns && imp.Workload.Name == "frontend" {
			found = true
			if len(imp.Chain) < 2 || imp.Chain[len(imp.Chain)-1] != "backend" {
				t.Errorf("impairment chain = %v, want it to end in backend", imp.Chain)
			}
		}
	}
	if !found {
		t.Fatalf("frontend not reported impaired in us-east-1a; impairments = %+v", dr.Impairments)
	}

	// us-east-1b is where backend is NOT: no impairment should be reported there.
	for i := range report.Domains {
		if report.Domains[i].Domain != "us-east-1b" {
			continue
		}
		for _, imp := range report.Domains[i].Impairments {
			if imp.Workload.Namespace == ns && imp.Workload.Name == "frontend" {
				t.Errorf("frontend reported impaired in us-east-1b, where backend is unaffected: %+v", imp)
			}
		}
	}
}

// clusterContext must match NewCluster's kubeconfig context naming, which
// this test uses directly rather than through Cluster.Apply because that
// method always applies into the manifest's own metadata.namespace (empty,
// i.e. "default", for the manifests in this file), and this test needs its
// own namespace for isolation and cleanup.
const clusterContext = "kwok-survive-depgraph"

// applyIn applies a manifest into a specific namespace.
func applyIn(t *testing.T, ns, manifest string) {
	t.Helper()
	cmd := exec.Command("kubectl", "--context", clusterContext, "-n", ns, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl apply -n %s failed: %v\n%s", ns, err, out)
	}
}

//go:build harness

package harness

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
)

// The prediction must match what the cluster actually does.
func TestPredictionMatchesRealDrain(t *testing.T) {
	c := NewCluster(t, "survive-diff")
	for _, n := range []struct{ name, zone string }{
		{"n-1a-0", "us-east-1a"}, {"n-1b-0", "us-east-1b"}, {"n-1c-0", "us-east-1c"},
	} {
		c.AddNode(t, n.name, n.zone)
	}

	c.Apply(t, testDeployment("concentrated", 2))
	c.Apply(t, spreadDeployment("spreadout", 3))
	c.WaitPodsReady(t, "default", 5, 90*time.Second)

	snap, err := snapshot.Fetch(context.Background(), c.Client)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	report := survive.Analyze(snap, domain.LabelZone)

	predicted := map[string]survive.Outcome{}
	for _, d := range report.Domains {
		if d.Domain != "us-east-1a" {
			continue
		}
		for _, v := range d.Verdicts {
			predicted[v.Workload.Name] = v.Outcome
		}
	}

	beforeSet := c.availableUIDsByWorkload(t)
	evicted, blocked := c.DrainZone(t, "us-east-1a")
	waitFor(t, 90*time.Second, "evicted pods to disappear", c.gone(evicted))
	afterSet := c.availableUIDsByWorkload(t)
	before, after := survivorsOf(beforeSet, afterSet)

	assertPredictionMatches(t, "TestPredictionMatchesRealDrain", predicted, before, after, c.blockedApps(t, blocked))
}

// A budget we call unsatisfiable must really block eviction with HTTP 429.
func TestUnsatisfiablePDBReallyBlocksEviction(t *testing.T) {
	c := NewCluster(t, "survive-pdb")
	c.AddNode(t, "n-1a-0", "us-east-1a")
	c.Apply(t, testDeployment("session-store", 1))
	c.Apply(t, `
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata: {name: session-store}
spec:
  minAvailable: 1
  selector: {matchLabels: {app: session-store}}
`)
	c.WaitPodsReady(t, "default", 1, 60*time.Second)
	waitFor(t, 60*time.Second, "the disruption controller to publish PDB status", func() bool {
		p, err := c.Client.PolicyV1().PodDisruptionBudgets("default").Get(
			context.Background(), "session-store", metav1.GetOptions{})
		return err == nil && p.Status.ObservedGeneration == p.Generation
	})

	snap, err := snapshot.Fetch(context.Background(), c.Client)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	report := survive.Analyze(snap, domain.LabelZone)

	predictedBlock := false
	for _, f := range report.PDBFindings {
		if f.PDB == "session-store" && f.Block != "" {
			predictedBlock = true
		}
	}
	if !predictedBlock {
		t.Fatal("did not predict that session-store's PDB blocks eviction")
	}

	pods, err := c.Client.CoreV1().Pods("default").List(context.Background(), listAll)
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("list pods: %v", err)
	}
	status, err := c.EvictPod(context.Background(), "default", pods.Items[0].Name)
	if err != nil {
		t.Fatalf("evict: %v", err)
	}
	if status != 429 {
		t.Errorf("eviction returned %d, want 429 (blocked by PDB)", status)
	}
}

// A refused eviction is not evidence about zone survivability: the engine
// models an involuntary domain outage (nodes vanish, budgets afford no
// protection), while a graceful drain respects the budget and never evicts
// the pod at all. All three of these must hold at once: the domain verdict
// says Lost, the PDB is reported as blocking, and the eviction really
// returns 429 — proving the harness compares the drain only where the
// eviction actually happened, instead of reading "PDB blocked it" as "the
// workload survived".
func TestBlockedEvictionIsNotEvidenceOfSurvival(t *testing.T) {
	c := NewCluster(t, "survive-blocked")
	c.AddNode(t, "n-1a-0", "us-east-1a")
	c.Apply(t, testDeployment("locked-down", 1))
	c.Apply(t, `
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata: {name: locked-down}
spec:
  minAvailable: 1
  selector: {matchLabels: {app: locked-down}}
`)
	c.WaitPodsReady(t, "default", 1, 60*time.Second)
	waitFor(t, 60*time.Second, "the disruption controller to publish PDB status", func() bool {
		p, err := c.Client.PolicyV1().PodDisruptionBudgets("default").Get(
			context.Background(), "locked-down", metav1.GetOptions{})
		return err == nil && p.Status.ObservedGeneration == p.Generation
	})

	snap, err := snapshot.Fetch(context.Background(), c.Client)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	report := survive.Analyze(snap, domain.LabelZone)

	// (a) the engine predicts OutcomeLost for the domain that will be drained.
	var outcome survive.Outcome
	found := false
	for _, d := range report.Domains {
		if d.Domain != "us-east-1a" {
			continue
		}
		for _, v := range d.Verdicts {
			if v.Workload.Name == "locked-down" {
				outcome = v.Outcome
				found = true
			}
		}
	}
	if !found || outcome != survive.OutcomeLost {
		t.Fatalf("outcome = %q, found = %v, want OutcomeLost: the zone genuinely disappears in an involuntary outage", outcome, found)
	}

	// (b) the engine reports the workload's PDB as blocking.
	predictedBlock := false
	for _, f := range report.PDBFindings {
		if f.PDB == "locked-down" && f.Block != "" {
			predictedBlock = true
		}
	}
	if !predictedBlock {
		t.Fatal("did not predict that locked-down's PDB blocks eviction")
	}

	// (c) the eviction really is refused with 429 by the real API server.
	evicted, blocked := c.DrainZone(t, "us-east-1a")
	if len(evicted) != 0 {
		t.Errorf("evicted = %v, want no pods actually evicted (the PDB should refuse it)", evicted)
	}
	if len(blocked) != 1 {
		t.Fatalf("blocked = %v, want exactly one refused eviction", blocked)
	}
}

// A satisfiable budget throttles simultaneous evictions but does not block
// them: the drain must complete, and nothing may be reported as blocked.
func TestSatisfiableBudgetThrottlesButDoesNotBlock(t *testing.T) {
	c := NewCluster(t, "survive-throttle")
	c.AddNode(t, "n-1a-0", "us-east-1a")
	c.AddNode(t, "n-1b-0", "us-east-1b")
	c.Apply(t, testDeployment("throttled", 4))
	c.Apply(t, `
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata: {name: throttled}
spec:
  minAvailable: 3
  selector: {matchLabels: {app: throttled}}
`)
	c.WaitPodsReady(t, "default", 4, 90*time.Second)
	waitFor(t, 60*time.Second, "the disruption controller to publish PDB status", func() bool {
		p, err := c.Client.PolicyV1().PodDisruptionBudgets("default").Get(
			context.Background(), "throttled", metav1.GetOptions{})
		return err == nil && p.Status.ObservedGeneration == p.Generation
	})

	_, blocked := c.DrainZone(t, "us-east-1a")
	if len(blocked) != 0 {
		t.Errorf("a satisfiable budget reported %d blocked eviction(s): %v; it should throttle and then yield",
			len(blocked), blocked)
	}
}

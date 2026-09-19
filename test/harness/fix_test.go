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
	"github.com/SaiPisey2/kubectl-survive/internal/verify"
)

// TestGeneratedFixSurvivesARealDrain is spec §10.2's proof: a fix this tool
// emits is applied to a live Deployment on a real KWOK cluster, and both the
// scheduling proof and the survivability proof are checked against what
// actually happens, not against the simulation alone. A fix that fails either
// half is the outage this tool exists to prevent, so a failure here is never
// weakened -- it is the most valuable result this suite can produce.
//
// The workload is deliberately a 3-replica Deployment with no spread
// constraint, concentrated in a single zone: that shape gets rung 1 (add an
// enforced topology spread constraint), which is proven full (schedulable and
// surviving), unlike the single-replica case where rung 4 alone is proven
// only partial.
func TestGeneratedFixSurvivesARealDrain(t *testing.T) {
	c := NewCluster(t, "survive-fixverify")
	ctx := context.Background()

	const ns = "fix-verify"
	const appName = "in-one-zone"
	const zoneA, zoneB, zoneC = "zone-a", "zone-b", "zone-c"

	c.ApplyNamespace(t, ns)
	t.Cleanup(func() { c.DeleteNamespace(t, ns) })

	// Only zone-a exists while the workload comes up, so all 3 replicas are
	// forced onto its one node -- the concentrated shape the fix must repair.
	c.AddNode(t, "n-"+zoneA+"-0", zoneA)
	c.Apply(t, withNamespace(testDeployment(appName, 3), ns))
	c.WaitPodsReady(t, ns, 3, 90*time.Second)

	// Now widen the cluster to 3 zones. The workload's pods stay exactly
	// where they are (Kubernetes never reschedules a Running pod on its
	// own), so the snapshot below sees 3 replicas concentrated in zone-a
	// against a cluster that actually has room to spread them.
	c.AddNode(t, "n-"+zoneB+"-0", zoneB)
	c.AddNode(t, "n-"+zoneC+"-0", zoneC)

	// KWOK briefly re-applies the standard not-ready:NoSchedule taint to a
	// freshly created node while it catches up to the Ready status already
	// set above; without this wait the analysis below intermittently sees
	// fewer usable zones than actually exist and reports the spread fix as
	// unschedulable, which is a race in this test's own setup, not a verdict
	// about the fix.
	c.WaitNodeSchedulable(t, "n-"+zoneB+"-0", 30*time.Second)
	c.WaitNodeSchedulable(t, "n-"+zoneC+"-0", 30*time.Second)

	snap, err := snapshot.Fetch(ctx, c.Client)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	report := survive.Analyze(snap, domain.LabelZone)

	if !workloadOutcomeIn(report, zoneA, appName, survive.OutcomeLost) {
		t.Fatalf("expected %s to be reported LOST losing %s before any fix", appName, zoneA)
	}

	results, err := verify.Analyze(ctx, snap, report, domain.LabelZone, nil)
	if err != nil {
		t.Fatalf("verify.Analyze: %v", err)
	}

	var full *verify.Verified
	for i := range results {
		if results[i].Workload.Name != appName {
			continue
		}
		for j := range results[i].Fixes {
			t.Logf("candidate rung %d %q: partial=%v schedulable=%v (%s) survives=%v (%s)",
				results[i].Fixes[j].Fix.Rung, results[i].Fixes[j].Fix.Title, results[i].Fixes[j].Partial,
				results[i].Fixes[j].Proof.Schedulable, results[i].Fixes[j].Proof.SchedulableDetail,
				results[i].Fixes[j].Proof.Survives, results[i].Fixes[j].Proof.SurvivesDetail)
			if !results[i].Fixes[j].Partial {
				full = &results[i].Fixes[j]
				break
			}
		}
	}
	if full == nil {
		t.Fatalf("engine or ladder bug: no full (non-partial) fix was produced for %s; "+
			"a 3-replica workload concentrated in one zone with no spread constraint must get "+
			"a full rung-1 fix", appName)
	}
	if !full.Proof.Schedulable || !full.Proof.Survives {
		t.Fatalf("a fix marked non-partial must be both schedulable and surviving, got %+v", full.Proof)
	}
	t.Logf("verified fix: rung %d %q — schedulable: %s; survives: %s",
		full.Fix.Rung, full.Fix.Title, full.Proof.SchedulableDetail, full.Proof.SurvivesDetail)

	// Apply the verified fix to the live Deployment: the same mutation the
	// verifier proved, applied for real, not re-derived here.
	dep, err := c.Client.AppsV1().Deployments(ns).Get(ctx, appName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	mutated := full.Fix.Apply(&dep.Spec.Template)
	dep.Spec.Template = *mutated
	if full.Fix.Replicas > 0 {
		replicas := int32(full.Fix.Replicas)
		dep.Spec.Replicas = &replicas
	}
	if _, err := c.Client.AppsV1().Deployments(ns).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("apply fix to live deployment: %v", err)
	}

	desired := 3
	if full.Fix.Replicas > 0 {
		desired = full.Fix.Replicas
	}

	// The rollout must fully replace the old ReplicaSet's pods -- not merely
	// keep `desired` pods Ready, which a rolling update satisfies from the
	// OLD replicas alone while the new ones are still coming up -- and never
	// leave one Pending. A fix that produces a Pending pod is the outage
	// this tool exists to prevent, so this is not weakened to "eventually"
	// or "mostly".
	waitFor(t, 120*time.Second, "rollout to finish with no pod pending", func() bool {
		return rolloutComplete(c, ns, appName, desired)
	})
	if pending := pendingPods(c, ns, appName); len(pending) > 0 {
		t.Fatalf("fix produced Pending pod(s), which is the outage this tool exists to prevent: %v", pending)
	}

	snap2, err := snapshot.Fetch(ctx, c.Client)
	if err != nil {
		t.Fatalf("snapshot after fix: %v", err)
	}
	report2 := survive.Analyze(snap2, domain.LabelZone)
	if workloadOutcomeIn(report2, zoneA, appName, survive.OutcomeLost) {
		t.Fatalf("%s is still reported LOST losing %s after applying the fix", appName, zoneA)
	}

	// Finally, prove it against a real drain, not just the simulation:
	// cordon and evict zone-a through the real eviction API, retrying
	// refused evictions the way kubectl drain does, and confirm a
	// surviving replica of this workload is still Ready afterward.
	beforeSet := c.availableUIDsByWorkload(t)
	evicted, _ := c.DrainZone(t, zoneA)
	waitFor(t, 90*time.Second, "evicted pods to disappear", c.gone(evicted))
	afterSet := c.availableUIDsByWorkload(t)
	_, after := survivorsOf(beforeSet, afterSet)

	if after[appName] == 0 {
		t.Fatalf("%s: fix was verified but no replica survived a real drain of %s", appName, zoneA)
	}
}

// workloadOutcomeIn reports whether report predicts outcome for workload in
// domain.
func workloadOutcomeIn(report *survive.Report, domainName, workloadName string, outcome survive.Outcome) bool {
	for _, d := range report.Domains {
		if d.Domain != domainName {
			continue
		}
		for _, v := range d.Verdicts {
			if v.Workload.Name == workloadName {
				return v.Outcome == outcome
			}
		}
	}
	return false
}

// rolloutComplete reports whether the Deployment's rolling update has fully
// finished: the controller has observed the latest spec, updated, available
// and total replica counts all equal `desired` (so no old-template pod is
// still lingering), and no current pod is Pending.
func rolloutComplete(c *Cluster, ns, appName string, desired int) bool {
	dep, err := c.Client.AppsV1().Deployments(ns).Get(context.Background(), appName, metav1.GetOptions{})
	if err != nil {
		return false
	}
	if dep.Status.ObservedGeneration < dep.Generation {
		return false
	}
	want := int32(desired)
	if dep.Status.UpdatedReplicas != want || dep.Status.AvailableReplicas != want || dep.Status.Replicas != want {
		return false
	}
	return len(pendingPods(c, ns, appName)) == 0
}

// pendingPods lists the names of every currently Pending pod for appName in
// ns.
func pendingPods(c *Cluster, ns, appName string) []string {
	pods, err := c.Client.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{
		LabelSelector: "app=" + appName,
	})
	if err != nil {
		return nil
	}
	var out []string
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == "Pending" {
			out = append(out, pods.Items[i].Name)
		}
	}
	return out
}

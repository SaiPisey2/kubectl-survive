//go:build harness

package harness

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/draincheck"
	"github.com/SaiPisey2/kubectl-survive/internal/sched"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// resetNodes deletes every Node object in the cluster. test/harness/scenario.go
// deliberately shares nodes across scenarios (they are cluster-scoped, unlike
// namespaced workloads) and always names zones deterministically
// ("zone-a", "zone-b", ...), so without a reset a scenario that used more
// zones than the current one leaves stale zone nodes behind and inflates the
// next scenario's analysed domain count. Nodes here are fake KWOK API objects,
// not real machines — deleting and recreating them is cheap and does not
// touch the one real cost (etcd/apiserver/controller-manager/scheduler
// startup) that the "reuse one cluster" rule protects.
func (c *Cluster) resetNodes(t *testing.T) {
	t.Helper()
	if err := c.Client.CoreV1().Nodes().DeleteCollection(
		context.Background(), metav1.DeleteOptions{}, metav1.ListOptions{},
	); err != nil {
		t.Fatalf("reset nodes: %v", err)
	}
}

// waitNamespaceGone waits for every pod in the namespace to actually
// disappear. DeleteNamespace issues an async, non-blocking delete
// (--wait=false, so the goroutine driving the sweep is never stuck behind
// finalizers); without waiting here, a still-terminating namespace's pods
// leak into the very next scenario's cluster-wide snapshot and pollute its
// verdicts with a workload that does not belong to it.
func (c *Cluster) waitNamespaceGone(t *testing.T, ns string) {
	t.Helper()
	waitFor(t, 60*time.Second, "namespace "+ns+" to empty", func() bool {
		pods, err := c.Client.CoreV1().Pods(ns).List(context.Background(), listAll)
		return err == nil && len(pods.Items) == 0
	})
}

// TestScenarioSweep reuses ONE cluster across every scenario. Creating a
// cluster per scenario dominates runtime and makes the nightly budget
// unreachable.
func TestScenarioSweep(t *testing.T) {
	count := envInt("SURVIVE_SCENARIOS", 20)
	offset := envInt("SURVIVE_SEED_OFFSET", 0)

	c := NewCluster(t, "survive-sweep")

	for i := 0; i < count; i++ {
		seed := int64(offset + i)
		t.Run("seed-"+strconv.FormatInt(seed, 10), func(t *testing.T) {
			c.resetNodes(t)

			sc := GenerateScenario(seed)
			ns := "s" + strconv.FormatInt(seed, 10)
			c.ApplyNamespace(t, ns)
			sc.ApplyIn(t, c, ns)
			c.WaitPodsReady(t, ns, sc.TotalReplicas(), 120*time.Second)

			snap, err := snapshot.Fetch(context.Background(), c.Client)
			if err != nil {
				t.Fatalf("seed %d: snapshot: %v", seed, err)
			}
			report := survive.Analyze(snap, domain.LabelZone)
			if len(report.Domains) != len(sc.Zones) {
				t.Fatalf("seed %d: analysed %d domains, scenario has %d zones",
					seed, len(report.Domains), len(sc.Zones))
			}

			// The static engine cannot see a satisfiable budget that still
			// deadlocks a real drain because the replacement pod has nowhere
			// to schedule outside the domain being drained (spec section 5.6);
			// that requires the scheduler framework, which is why this is built
			// here rather than folded into survive.Analyze itself.
			dsc, err := sched.New(context.Background(), snap)
			if err != nil {
				t.Fatalf("seed %d: build scheduler: %v", seed, err)
			}
			deadlocks, err := draincheck.Detect(context.Background(), dsc, snap, domain.LabelZone)
			if err != nil {
				t.Fatalf("seed %d: draincheck.Detect: %v", seed, err)
			}

			// Every verdict must be a real outcome, never an empty string.
			for _, d := range report.Domains {
				for _, v := range d.Verdicts {
					switch v.Outcome {
					case survive.OutcomeSurvives, survive.OutcomeDegraded, survive.OutcomeLost, survive.OutcomeUnknown:
					default:
						t.Errorf("seed %d: %s has empty outcome", seed, v.Workload)
					}
				}
			}

			// The predictions are worthless unless they are actually checked
			// against a real drain: pull out the first zone's predictions,
			// drain it for real, and compare. Namespace deletion between
			// iterations is async (DeleteNamespace uses --wait=false) and
			// waitNamespaceGone only confirms the PREVIOUS namespace's PODS
			// are gone, not that its other namespaced objects (a PDB, say)
			// have finished being garbage-collected. A still-terminating
			// namespace's now-podless PDB reports BlockNoPodsMatched, which
			// is a non-empty Block — so PDBFindings must be scoped to this
			// iteration's own namespace, or a leftover PDB from an earlier
			// seed falsely inflates predictedBlock below.
			predicted := map[string]survive.Outcome{}
			for _, d := range report.Domains {
				if d.Domain != sc.Zones[0] {
					continue
				}
				for _, v := range d.Verdicts {
					if v.Workload.Namespace != ns {
						continue
					}
					predicted[v.Workload.Name] = v.Outcome
				}
			}

			beforeSet := c.availableUIDsByWorkload(t)
			evicted, blocked := c.DrainZone(t, sc.Zones[0])
			waitFor(t, 90*time.Second, "evicted pods to disappear", c.gone(evicted))
			afterSet := c.availableUIDsByWorkload(t)
			before, after := survivorsOf(beforeSet, afterSet)
			blockedBy := c.blockedApps(t, blocked)
			assertPredictionMatches(t, fmt.Sprintf("seed %d", seed), predicted, before, after, blockedBy)

			// Each generated PDB is named after its workload, and that name is
			// also the pods' app label, so findings map directly to workloads.
			// Namespace-scoped: a still-terminating earlier namespace's
			// now-podless PDB reports BlockNoPodsMatched (a non-empty Block),
			// which would otherwise falsely satisfy the per-workload check below.
			predictedBlock := map[string]bool{}
			for _, f := range report.PDBFindings {
				if f.Namespace != ns {
					continue
				}
				if f.Block != "" {
					predictedBlock[f.PDB] = true
				}
			}

			// The scheduler-backed check catches exactly the case the static one
			// cannot: a satisfiable budget whose replacement pod has nowhere to
			// schedule outside the domain being drained. Its findings merge into
			// the same predictedBlock map so the two per-workload checks below do
			// not need to know which detector caught which workload.
			for _, f := range deadlocks {
				if f.Namespace != ns {
					continue
				}
				predictedBlock[f.PDB] = true
			}

			// Every workload whose eviction was actually refused must have had
			// its budget flagged. Without this per-workload check, skipping a
			// blocked workload in the survivor comparison could hide a real
			// false negative in PDB detection: workload A's blocking PDB being
			// correctly flagged does not prove workload B's was too, and a
			// namespace-aggregate count could not tell the two apart.
			for app := range blockedBy {
				if predictedBlock[app] {
					continue
				}
				if c.unschedulableReplacement(t, app) {
					// unschedulableReplacement stays as an observational cross-check:
					// it independently confirms, from the real cluster's Pending
					// condition, that a replacement genuinely could not schedule.
					// draincheck.Detect above is now expected to have predicted this
					// case, so reaching here with predictedBlock[app] still false is a
					// real miss in the detector, not a known, accepted gap.
					t.Errorf("seed %d: %s's replacement was unschedulable but draincheck did not predict it",
						seed, app)
					continue
				}
				t.Errorf("seed %d: %s had an eviction persistently refused but the engine did not flag its PDB as blocking",
					seed, app)
			}
			// The reverse stays aggregate on purpose: a budget can be correctly
			// flagged as blocking without being exercised, because none of its
			// pods sat in the drained zone.
			if len(predictedBlock) == 0 && len(blocked) > 0 {
				t.Errorf("seed %d: predicted no blocking PDB but %d eviction(s) were refused: %v",
					seed, len(blocked), blocked)
			}

			c.DeleteNamespace(t, ns)
			c.waitNamespaceGone(t, ns)
		})
	}
}

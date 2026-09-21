//go:build harness

package harness

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var listAll = metav1.ListOptions{}

// EvictPod calls the eviction subresource and returns the HTTP status code.
// A real API server answers 201 on success; the client-go call above only
// returns an error, so a nil error is normalized to 200 here for the caller.
// 429 means a PDB blocked it, 500 means the pod is selected by more than one
// PDB.
func (c *Cluster) EvictPod(ctx context.Context, namespace, name string) (int, error) {
	err := c.Client.PolicyV1().Evictions(namespace).Evict(ctx, &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	})
	if err == nil {
		return 200, nil
	}
	if statusErr, ok := err.(apierrors.APIStatus); ok {
		return int(statusErr.Status().Code), nil
	}
	return 0, err
}

// evictionRetryWindow bounds how long we retry a refused eviction. Real
// kubectl drain retries indefinitely; a budget that is merely throttling
// yields within a second or two once a replacement becomes Ready, while a
// never-satisfiable budget refuses forever.
const evictionRetryWindow = 20 * time.Second

// evictWithRetry mirrors kubectl drain: a 429 means "not right now", so it is
// retried until the window closes. Only then is the pod treated as blocked.
// The final status is returned so callers can distinguish 429 from 500.
func (c *Cluster) evictWithRetry(ctx context.Context, namespace, name string) (int, error) {
	deadline := time.Now().Add(evictionRetryWindow)
	for {
		status, err := c.EvictPod(ctx, namespace, name)
		if err != nil {
			return status, err
		}
		if status != 429 || time.Now().After(deadline) {
			return status, nil
		}
		time.Sleep(time.Second)
	}
}

// DrainZone cordons every node in the zone and evicts its pods, recording which
// evictions succeeded and which were refused.
func (c *Cluster) DrainZone(t *testing.T, zone string) (evicted, blocked []string) {
	t.Helper()
	ctx := context.Background()

	nodes, err := c.Client.CoreV1().Nodes().List(ctx, listAll)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}

	for i := range nodes.Items {
		n := &nodes.Items[i]
		if n.Labels["topology.kubernetes.io/zone"] != zone {
			continue
		}
		n.Spec.Unschedulable = true
		if _, err := c.Client.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("cordon %s: %v", n.Name, err)
		}

		pods, err := c.Client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
			FieldSelector: "spec.nodeName=" + n.Name,
		})
		if err != nil {
			t.Fatalf("list pods on %s: %v", n.Name, err)
		}
		for j := range pods.Items {
			p := &pods.Items[j]
			status, err := c.evictWithRetry(ctx, p.Namespace, p.Name)
			if err != nil {
				t.Fatalf("evict %s: %v", p.Name, err)
			}
			if status == 200 {
				evicted = append(evicted, p.Namespace+"/"+p.Name)
			} else {
				blocked = append(blocked, fmt.Sprintf("%s/%s (HTTP %d)", p.Namespace, p.Name, status))
			}
		}
	}
	return evicted, blocked
}

// availableUIDsByWorkload returns, per app label, the set of currently Ready
// pod UIDs. Comparing UID sets rather than counts means replacement pods
// created after a drain cannot mask the loss of the originals.
func (c *Cluster) availableUIDsByWorkload(t *testing.T) map[string]map[types.UID]bool {
	t.Helper()
	pods, err := c.Client.CoreV1().Pods(metav1.NamespaceAll).List(context.Background(), listAll)
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	out := map[string]map[types.UID]bool{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				app := p.Labels["app"]
				if out[app] == nil {
					out[app] = map[types.UID]bool{}
				}
				out[app][p.UID] = true
				break // count each pod once
			}
		}
	}
	return out
}

// survivorsOf counts how many of the pre-drain pods are still Ready.
func survivorsOf(before, after map[string]map[types.UID]bool) (beforeN, afterN map[string]int) {
	beforeN, afterN = map[string]int{}, map[string]int{}
	for app, uids := range before {
		beforeN[app] = len(uids)
		n := 0
		for uid := range uids {
			if after[app][uid] {
				n++
			}
		}
		afterN[app] = n
	}
	return beforeN, afterN
}

// Note: a pod reaches `blocked` only after DrainZone's retry window has closed,
// so it means "persistently refused", not "throttled for a moment". A healthy
// budget legitimately refuses a simultaneous second eviction and then allows it
// on retry; only a budget that can never be satisfied refuses indefinitely.

// blockedApps returns the app labels of pods whose eviction the API server
// refused. A drain that never happened is not evidence about zone loss: the
// engine models an involuntary domain outage, in which a PodDisruptionBudget
// affords no protection at all, while a graceful drain respects it. Those
// workloads are therefore compared on the PDB prediction instead.
func (c *Cluster) blockedApps(t *testing.T, blocked []string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, entry := range blocked {
		key, _, _ := strings.Cut(entry, " ") // "ns/name (HTTP 429)" -> "ns/name"
		ns, name, found := strings.Cut(key, "/")
		if !found {
			continue
		}
		pod, err := c.Client.CoreV1().Pods(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			continue // already gone; it was not really blocked
		}
		if app := pod.Labels["app"]; app != "" {
			out[app] = true
		}
	}
	return out
}

// unschedulableReplacement reports whether a workload has a replacement pod
// stuck Pending — the signature of a drain deadlock that emerges from the
// interaction of a disruption budget with an enforced spread constraint during
// a zone evacuation. internal/draincheck now predicts this class from the
// scheduler framework, so sweep_test.go expects it to already appear in
// predictedBlock by the time this is consulted; this stays as an independent,
// observational cross-check against the real cluster's Pending condition,
// not as the thing that recognises the gap.
func (c *Cluster) unschedulableReplacement(t *testing.T, app string) bool {
	t.Helper()
	pods, err := c.Client.CoreV1().Pods(metav1.NamespaceAll).List(context.Background(), listAll)
	if err != nil {
		return false
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Labels["app"] != app || p.Status.Phase != corev1.PodPending {
			continue
		}
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
				return true
			}
		}
	}
	return false
}

// assertPredictionMatches checks each predicted outcome against what the
// cluster actually did. before/after count the workload's own pre-drain
// replicas that are still Ready. blocked names the app labels whose
// eviction was refused by a PDB; those are skipped here since a refused
// eviction never happened and tells us nothing about zone survivability.
func assertPredictionMatches(t *testing.T, label string, predicted map[string]survive.Outcome, before, after map[string]int, blocked map[string]bool) {
	t.Helper()
	for name, want := range predicted {
		if blocked[name] {
			// The drain never evicted this workload's pods, so it tells us
			// nothing about whether the workload survives losing the domain.
			// The PDB prediction is asserted separately.
			continue
		}
		actualLost := before[name] > 0 && after[name] == 0
		switch want {
		case survive.OutcomeLost:
			if !actualLost {
				t.Errorf("%s: %s predicted LOST, but %d replicas remained", label, name, after[name])
			}
		case survive.OutcomeSurvives:
			if actualLost {
				t.Errorf("%s: %s predicted SURVIVES, but every replica went away", label, name)
			}
		case survive.OutcomeDegraded:
			if after[name] == 0 {
				t.Errorf("%s: %s predicted DEGRADED, but every replica went away", label, name)
			}
		case survive.OutcomeUnknown:
			t.Errorf("%s: %s predicted UNKNOWN, but every node in this scenario carries a zone label", label, name)
		default:
			t.Errorf("%s: %s has unhandled outcome %q", label, name, want)
		}
	}
}

// spreadDeployment renders a Deployment with an enforced zone spread constraint.
func spreadDeployment(name string, replicas int) string {
	return fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata: {name: %s}
spec:
  replicas: %d
  selector: {matchLabels: {app: %s}}
  template:
    metadata: {labels: {app: %s}}
    spec:
      tolerations: [{key: kwok.x-k8s.io/node, operator: Exists, effect: NoSchedule}]
      topologySpreadConstraints:
      - maxSkew: 1
        topologyKey: topology.kubernetes.io/zone
        whenUnsatisfiable: DoNotSchedule
        labelSelector: {matchLabels: {app: %s}}
      containers:
      - name: c
        image: nginx
        resources: {requests: {cpu: 100m, memory: 128Mi}}
`, name, replicas, name, name, name)
}

// waitFor polls until cond returns true, failing the test if it never does.
// Bare sleeps make the differential tests flaky on slow machines; a condition
// that is already satisfied also returns immediately, so this is faster.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// gone reports whether every named pod ("namespace/name") has disappeared.
func (c *Cluster) gone(keys []string) func() bool {
	return func() bool {
		for _, key := range keys {
			ns, name, found := strings.Cut(key, "/")
			if !found {
				continue
			}
			if _, err := c.Client.CoreV1().Pods(ns).Get(context.Background(), name, metav1.GetOptions{}); err == nil {
				return false
			}
		}
		return true
	}
}

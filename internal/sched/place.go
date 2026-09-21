package sched

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
)

// Placement is where a workload's replicas would land.
type Placement struct {
	Nodes         []string       // one node name per scheduled replica
	Domains       map[string]int // replicas per failure domain
	Unschedulable int
	Reason        string // why the first unschedulable replica had nowhere to go
}

// Place simulates scheduling `replicas` copies of a pod template.
//
// Each replica is placed, written into the scheduler cache and made visible to
// the plugins before the next is considered. Without that feedback a spread
// constraint has nothing to measure against and every replica independently
// picks the same node: three replicas under maxSkew=1 land 3/0/0 rather than
// 1/1/1.
//
// Placement is deliberately first-fit over the Filter verdict, and never
// consults the Score plugins. Filter is what the scheduler MUST honour; Score
// is what it MAY prefer. A workload that ends up spread only because scoring
// happened to favour an empty zone has no guarantee at all, and claiming it
// survives would be exactly the false assurance this tool exists to remove. The
// effect is that simulation is pessimistic: it credits enforcement, not luck.
//
// The cluster view is restored before returning, so the Scheduler keeps
// describing the cluster as it actually is.
func (s *Scheduler) Place(ctx context.Context, pod *corev1.Pod, replicas int, domainKey string) (_ Placement, err error) {
	if replicas <= 0 {
		return Placement{}, fmt.Errorf("replicas must be positive, got %d", replicas)
	}
	if pod.Spec.NodeName != "" {
		return Placement{}, fmt.Errorf("Place needs an unassigned template; got one bound to %s", pod.Spec.NodeName)
	}

	var simulated []*corev1.Pod
	defer func() {
		if rerr := s.withdraw(simulated); rerr != nil && err == nil {
			// A cache left holding phantom pods would silently corrupt every
			// later answer, so this is fatal rather than logged.
			err = fmt.Errorf("restoring the cluster view: %w", rerr)
		}
	}()

	byName := make(map[string]*corev1.Node, len(s.nodes))
	for _, n := range s.nodes {
		byName[n.Name] = n
	}

	out := Placement{Domains: map[string]int{}}
	for i := 0; i < replicas; i++ {
		candidate := simTemplate(pod, i)

		fits, ferr := s.Fits(ctx, candidate, domainKey)
		if ferr != nil {
			return Placement{}, fmt.Errorf("replica %d: %w", i, ferr)
		}

		chosen, reason := firstFit(fits)
		if chosen == "" {
			out.Unschedulable++
			if out.Reason == "" {
				out.Reason = reason
			}
			continue
		}

		bound := candidate.DeepCopy()
		bound.Spec.NodeName = chosen
		bound.Status.Phase = corev1.PodRunning
		if aerr := s.cache.AddPod(s.logger, bound); aerr != nil {
			return Placement{}, fmt.Errorf("replica %d: place on %s: %w", i, chosen, aerr)
		}
		simulated = append(simulated, bound)
		if uerr := s.cache.UpdateSnapshot(s.logger, s.snapshot); uerr != nil {
			return Placement{}, fmt.Errorf("replica %d: refresh snapshot: %w", i, uerr)
		}

		out.Nodes = append(out.Nodes, chosen)
		if d, ok := domain.Of(byName[chosen], domainKey); ok {
			out.Domains[d]++
		}
	}
	return out, nil
}

// PlaceReplacing simulates the steady state after a rollout: replacing's
// pods are withdrawn from the cluster view before the replicas are placed,
// and restored again before returning.
//
// A fix to an existing workload replaces its pods; it does not add a second
// copy of them alongside the ones already there. Place alone models "add N
// more pods to the cluster as it stands", which is correct for a workload
// that does not exist yet but double-counts an existing one against itself:
// its old pods still occupy capacity and still count toward topology spread
// while the new ones are being fit in. On a cluster with slack that merely
// produces a placement that will not be the real one; on a tight cluster it
// wrongly refuses a fix that would in fact fit once the rollout completes.
//
// replacing is exactly the set of pods the caller believes this workload's
// rollout will remove; only the ones actually occupying a node are
// withdrawn, mirroring how the cache was seeded from assigned pods only.
func (s *Scheduler) PlaceReplacing(ctx context.Context, pod *corev1.Pod, replicas int, domainKey string, replacing []*corev1.Pod) (_ Placement, err error) {
	withdrawn, werr := s.withdrawExisting(replacing)
	if werr != nil {
		return Placement{}, fmt.Errorf("withdrawing the pods being replaced: %w", werr)
	}
	defer func() {
		if rerr := s.reinstate(withdrawn); rerr != nil && err == nil {
			// Leaving the workload's real pods out of the cache would corrupt
			// every later answer just as surely as leaving a simulated one in.
			err = fmt.Errorf("restoring the replaced pods: %w", rerr)
		}
	}()
	return s.Place(ctx, pod, replicas, domainKey)
}

// FitsReplacing reports where pod could schedule with replacing's pods
// withdrawn from the cluster view first, mirroring PlaceReplacing's
// eviction-aware accounting but returning the raw per-node Filter verdicts
// instead of choosing and committing to one.
//
// PlaceReplacing's first-fit is the right choice when the caller only needs
// to know "does this fit somewhere" -- a rollout doesn't care which of the
// acceptable nodes it lands on. Some callers need more: a domain-drain check
// must know not merely whether a replacement fits, but whether it fits
// *outside a specific domain*, since a real drain cordons every node in that
// domain rather than removing it from the cluster. Excluding a domain by
// giving the candidate a node-affinity term looks equivalent but is not: the
// PodTopologySpread plugin's default NodeAffinityPolicy (Honor) drops any
// domain the pod's own affinity already excludes from its skew computation
// entirely, which loosens the very constraint the caller is trying to
// evaluate honestly and produces false negatives. Filtering the *results* of
// an unmodified Fits call, after the eviction-aware withdrawal, keeps the
// spread plugin computing skew against the real domain layout while still
// letting the caller reject nodes in the domain being drained.
func (s *Scheduler) FitsReplacing(ctx context.Context, pod *corev1.Pod, domainKey string, replacing []*corev1.Pod) (_ []Fit, err error) {
	withdrawn, werr := s.withdrawExisting(replacing)
	if werr != nil {
		return nil, fmt.Errorf("withdrawing the pods being replaced: %w", werr)
	}
	defer func() {
		if rerr := s.reinstate(withdrawn); rerr != nil && err == nil {
			// Leaving the workload's real pods out of the cache would corrupt
			// every later answer just as surely as leaving a simulated one in.
			err = fmt.Errorf("restoring the replaced pods: %w", rerr)
		}
	}()
	return s.Fits(ctx, pod, domainKey)
}

// withdrawExisting removes pods' assigned members from the cache ahead of a
// PlaceReplacing simulation, returning exactly the pods it removed so they
// can be put back.
func (s *Scheduler) withdrawExisting(pods []*corev1.Pod) ([]*corev1.Pod, error) {
	assigned := assignedPods(pods)
	if len(assigned) == 0 {
		return nil, nil
	}
	for _, p := range assigned {
		if err := s.cache.RemovePod(s.logger, p); err != nil {
			return nil, fmt.Errorf("remove existing pod %s/%s: %w", p.Namespace, p.Name, err)
		}
	}
	if err := s.cache.UpdateSnapshot(s.logger, s.snapshot); err != nil {
		return nil, fmt.Errorf("refresh snapshot: %w", err)
	}
	return assigned, nil
}

// reinstate is withdrawExisting's inverse, adding pods back to the cache and
// refreshing the snapshot so the plugins see them again.
func (s *Scheduler) reinstate(pods []*corev1.Pod) error {
	if len(pods) == 0 {
		return nil
	}
	for _, p := range pods {
		if err := s.cache.AddPod(s.logger, p); err != nil {
			return fmt.Errorf("re-add existing pod %s/%s: %w", p.Namespace, p.Name, err)
		}
	}
	return s.cache.UpdateSnapshot(s.logger, s.snapshot)
}

// withdraw removes the simulated pods and refreshes the snapshot, in reverse
// order of placement.
func (s *Scheduler) withdraw(pods []*corev1.Pod) error {
	for i := len(pods) - 1; i >= 0; i-- {
		if err := s.cache.RemovePod(s.logger, pods[i]); err != nil {
			return fmt.Errorf("remove simulated pod %s: %w", pods[i].Name, err)
		}
	}
	if len(pods) == 0 {
		return nil
	}
	return s.cache.UpdateSnapshot(s.logger, s.snapshot)
}

// simTemplate produces one distinctly identified copy of the template. The
// cache keys pods by UID, so reusing one would silently place a single pod
// repeatedly.
func simTemplate(pod *corev1.Pod, i int) *corev1.Pod {
	c := pod.DeepCopy()
	c.Name = fmt.Sprintf("%s-sim-%d", pod.Name, i)
	c.UID = types.UID(fmt.Sprintf("%s-sim-%d", pod.UID, i))
	c.ResourceVersion = ""
	c.CreationTimestamp = metav1.Time{}
	c.Spec.NodeName = ""
	return c
}

// firstFit takes the first node the Filter plugins accepted, and otherwise the
// first rejection reason, which is the most useful one to show an operator.
func firstFit(fits []Fit) (node string, reason string) {
	for _, f := range fits {
		if f.OK {
			return f.Node, ""
		}
		if reason == "" {
			reason = f.Reason
		}
	}
	if reason == "" {
		reason = "no node accepted the pod"
	}
	return "", reason
}

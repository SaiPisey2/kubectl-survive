// Package pdbcheck finds PodDisruptionBudgets that can never permit an
// eviction. These do not cause outages; they cause node drains to hang
// forever, which surfaces as an upgrade that never finishes.
package pdbcheck

import (
	"fmt"

	"github.com/SaiPisey2/kubectl-survive/internal/health"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type Block string

const (
	BlockNone             Block = ""
	BlockNeverSatisfiable Block = "NeverSatisfiable"
	BlockMultiplePDBs     Block = "MultiplePDBs"
	BlockNoPodsMatched    Block = "NoPodsMatched"
	BlockStaleStatus      Block = "StaleStatus"
)

type Finding struct {
	PDB       string
	Namespace string
	Block     Block
	Detail    string
}

// requiredAvailable resolves minAvailable against a replica count.
// Percentages round UP, matching GetScaledValueFromIntOrPercent(roundUp=true).
func requiredAvailable(minAvailable *intstr.IntOrString, replicas int) int {
	if minAvailable == nil {
		return 0
	}
	v, err := intstr.GetScaledValueFromIntOrPercent(minAvailable, replicas, true)
	if err != nil {
		return 0
	}
	return v
}

// allowedUnavailable resolves maxUnavailable. Percentages also round UP.
// Returns -1 when the field is not configured.
func allowedUnavailable(maxUnavailable *intstr.IntOrString, replicas int) int {
	if maxUnavailable == nil {
		return -1
	}
	v, err := intstr.GetScaledValueFromIntOrPercent(maxUnavailable, replicas, true)
	if err != nil {
		return -1
	}
	return v
}

func Analyze(s *snapshot.Snapshot) []Finding {
	// Count how many PDBs select each pod: more than one is a permanent HTTP
	// 500 on eviction, which kubectl drain's 429-retry loop never recovers from.
	pdbsPerPod := map[string]int{}
	matched := map[string][]*corev1.Pod{}

	for _, p := range s.PDBs {
		sel, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
		if err != nil {
			continue
		}
		key := p.Namespace + "/" + p.Name
		for _, pod := range s.Pods {
			if pod.Namespace != p.Namespace || !sel.Matches(labels.Set(pod.Labels)) {
				continue
			}
			// Terminating, Pending, Succeeded and Failed pods are never
			// subject to the budget (the API server's canIgnorePDB skips
			// them outright), so they must not inflate the denominator: a
			// replicas:1, minAvailable:1 Deployment that is momentarily
			// running 2 pods mid-rollout is never-satisfiable against its
			// desired count, not "satisfiable" against a transient 2.
			if health.IgnoredByPDB(pod) {
				continue
			}
			matched[key] = append(matched[key], pod)
			pdbsPerPod[pod.Namespace+"/"+pod.Name]++
		}
	}

	var out []Finding
	for _, p := range s.PDBs {
		key := p.Namespace + "/" + p.Name
		pods := matched[key]
		f := Finding{PDB: p.Name, Namespace: p.Namespace, Block: BlockNone}

		switch {
		case len(pods) == 0:
			f.Block = BlockNoPodsMatched
			f.Detail = "selector matches no pods; this budget protects nothing"

		case anyPodHasMultiplePDBs(pods, pdbsPerPod):
			f.Block = BlockMultiplePDBs
			f.Detail = "a pod is selected by more than one PDB; eviction returns HTTP 500 and drains never complete"

		case p.Generation != p.Status.ObservedGeneration:
			// The API server itself refuses eviction while status is stale.
			f.Block = BlockStaleStatus
			f.Detail = fmt.Sprintf("status is stale (generation %d, observed %d); evictions are refused until it reconciles",
				p.Generation, p.Status.ObservedGeneration)

		default:
			total := len(pods)
			if need := requiredAvailable(p.Spec.MinAvailable, total); p.Spec.MinAvailable != nil && need >= total {
				f.Block = BlockNeverSatisfiable
				f.Detail = fmt.Sprintf("minAvailable resolves to %d of %d pods; no pod can ever be evicted", need, total)
			} else if allowedUnavailable(p.Spec.MaxUnavailable, total) == 0 {
				f.Block = BlockNeverSatisfiable
				f.Detail = "maxUnavailable resolves to 0; no pod can ever be evicted"
			}
		}
		out = append(out, f)
	}
	return out
}

func anyPodHasMultiplePDBs(pods []*corev1.Pod, counts map[string]int) bool {
	for _, pod := range pods {
		if counts[pod.Namespace+"/"+pod.Name] > 1 {
			return true
		}
	}
	return false
}

// DesiredAvailable reports how many pods must remain available for the budget
// that selects this pod, and whether any budget selects it at all. Percentages
// resolve against replicas and round up, matching the API server.
//
// A non-nil error means the budget's selector could not be parsed: the
// caller must NOT treat that as "no budget" (which reads as safe) — an
// unparseable budget's effect on drains is unknown, and unknown must never
// render as survives.
func DesiredAvailable(s *snapshot.Snapshot, pod *corev1.Pod, replicas int) (int, bool, error) {
	for _, p := range s.PDBs {
		if p.Namespace != pod.Namespace {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
		if err != nil {
			return 0, false, fmt.Errorf("PDB %s/%s has an unparseable selector: %w", p.Namespace, p.Name, err)
		}
		if !sel.Matches(labels.Set(pod.Labels)) {
			continue
		}
		switch {
		case p.Spec.MinAvailable != nil:
			return requiredAvailable(p.Spec.MinAvailable, replicas), true, nil
		case p.Spec.MaxUnavailable != nil:
			if allowed := allowedUnavailable(p.Spec.MaxUnavailable, replicas); allowed >= 0 {
				need := replicas - allowed
				if need < 0 {
					need = 0
				}
				return need, true, nil
			}
		}
	}
	return 0, false, nil
}

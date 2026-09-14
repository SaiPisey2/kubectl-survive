// Package volumepin detects pods that can never leave a failure domain because
// their PersistentVolume is pinned to it.
//
// This overrides any spread or anti-affinity analysis: a manifest change cannot
// move a zonal disk, so the workload is structurally single-domain.
package volumepin

import (
	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	corev1 "k8s.io/api/core/v1"
)

type Pin struct {
	PVC    string
	PV     string
	Domain string
}

// ForPod returns one Pin per volume that constrains the pod to a single domain.
func ForPod(s *snapshot.Snapshot, pod *corev1.Pod, domainKey string) []Pin {
	pvByName := map[string]*corev1.PersistentVolume{}
	for _, pv := range s.PVs {
		pvByName[pv.Name] = pv
	}
	pvcByKey := map[string]*corev1.PersistentVolumeClaim{}
	for _, c := range s.PVCs {
		pvcByKey[c.Namespace+"/"+c.Name] = c
	}

	var pins []Pin
	for _, vol := range pod.Spec.Volumes {
		if vol.PersistentVolumeClaim == nil {
			continue
		}
		pvc, ok := pvcByKey[pod.Namespace+"/"+vol.PersistentVolumeClaim.ClaimName]
		if !ok || pvc.Spec.VolumeName == "" {
			continue
		}
		pv, ok := pvByName[pvc.Spec.VolumeName]
		if !ok {
			continue
		}
		if d, ok := singleDomainOf(pv, domainKey); ok {
			pins = append(pins, Pin{PVC: pvc.Name, PV: pv.Name, Domain: d})
		}
	}
	return pins
}

// singleDomainOf returns the domain a PV is pinned to, if it is pinned to
// exactly one. Both the stable and deprecated zone labels are checked, because
// a PV can long outlive the label migration that created it.
//
// NodeSelectorTerms are ORed by Kubernetes, so the volume is pinned only when
// EVERY term pins it to the SAME single domain. One term without a domain
// constraint, or two terms naming different domains, leaves the volume
// reachable from more than one place and therefore unpinned.
func singleDomainOf(pv *corev1.PersistentVolume, domainKey string) (string, bool) {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return "", false
	}
	terms := pv.Spec.NodeAffinity.Required.NodeSelectorTerms
	if len(terms) == 0 {
		return "", false
	}

	keys := []string{domainKey}
	if domainKey == domain.LabelZone {
		keys = append(keys, domain.LabelZoneDeprecated)
	}

	pinned := ""
	for _, term := range terms {
		found := ""
		for _, expr := range term.MatchExpressions {
			for _, k := range keys {
				if expr.Key == k && expr.Operator == corev1.NodeSelectorOpIn && len(expr.Values) == 1 {
					found = expr.Values[0]
				}
			}
		}
		if found == "" {
			// This term permits nodes in any domain.
			return "", false
		}
		if pinned == "" {
			pinned = found
		} else if pinned != found {
			// Terms are ORed, so two different domains means not pinned.
			return "", false
		}
	}
	return pinned, true
}

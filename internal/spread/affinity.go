package spread

import corev1 "k8s.io/api/core/v1"

// ClassifyAntiAffinity reports what guarantee a workload's pod anti-affinity
// gives for the requested domain key.
//
// Required terms are a real admission-time guarantee; preferred terms only
// affect scoring and are therefore advisory. Both are IgnoredDuringExecution,
// so neither is re-checked once a pod is running: this is why placement must be
// read from the cluster rather than inferred from the spec.
func ClassifyAntiAffinity(affinity *corev1.Affinity, domainKey string) Assessment {
	if affinity == nil || affinity.PodAntiAffinity == nil {
		return Assessment{StateAbsent, "no pod anti-affinity"}
	}
	aa := affinity.PodAntiAffinity

	for _, t := range aa.RequiredDuringSchedulingIgnoredDuringExecution {
		if t.TopologyKey == domainKey {
			return Assessment{StateEnforced, "required pod anti-affinity on " + domainKey}
		}
	}
	for _, w := range aa.PreferredDuringSchedulingIgnoredDuringExecution {
		if w.PodAffinityTerm.TopologyKey == domainKey {
			return Assessment{StateAdvisory, "anti-affinity is preferred, not required, so it gives no guarantee"}
		}
	}
	return Assessment{StateAbsent, "no pod anti-affinity for this domain key"}
}

// Package spread classifies how much real protection a workload's topology
// spread configuration provides.
//
// A constraint being present is not the same as a constraint being useful:
// maxSkew at or above the replica count permits every replica to land in one
// domain while the constraint still reports as satisfied.
package spread

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

type State string

const (
	StateEnforced        State = "enforced"
	StateEnforcedButWeak State = "enforced-but-weak"
	StateAdvisory        State = "advisory"
	StateAbsent          State = "absent"
	StateUnknown         State = "unknown"
)

type Assessment struct {
	State  State
	Detail string
}

// Classify evaluates the constraints that apply to the given domain key.
//
// Cluster-level default constraints are deliberately NOT assumed. The
// scheduler's configuration is not readable through any Kubernetes API, and
// both system defaults use ScheduleAnyway, so they would be advisory anyway.
func Classify(constraints []corev1.TopologySpreadConstraint, domainKey string, replicas, domainCount int) Assessment {
	var enforced, advisory *corev1.TopologySpreadConstraint
	for i := range constraints {
		if constraints[i].TopologyKey != domainKey {
			continue
		}
		if constraints[i].WhenUnsatisfiable == corev1.DoNotSchedule {
			// The scheduler ANDs every constraint, so the tightest maxSkew is
			// the one that binds. Taking the last match instead would make the
			// verdict depend on slice order.
			if enforced == nil || constraints[i].MaxSkew < enforced.MaxSkew {
				enforced = &constraints[i]
			}
			continue
		}
		if advisory == nil {
			advisory = &constraints[i]
		}
	}

	switch {
	case enforced != nil:
		if domainCount <= 1 {
			return Assessment{StateEnforcedButWeak, fmt.Sprintf(
				"only %d domain(s) carry %s, so no spread is possible", domainCount, domainKey)}
		}
		if enforced.MinDomains != nil && int(*enforced.MinDomains) > 1 && domainCount >= int(*enforced.MinDomains) {
			return Assessment{StateEnforced, fmt.Sprintf(
				"minDomains %d forces spread across at least that many domains", *enforced.MinDomains)}
		}
		if int(enforced.MaxSkew) >= replicas {
			return Assessment{StateEnforcedButWeak, fmt.Sprintf(
				"maxSkew %d with %d replicas permits every replica in one domain", enforced.MaxSkew, replicas)}
		}
		return Assessment{StateEnforced, fmt.Sprintf("maxSkew %d on %s", enforced.MaxSkew, domainKey)}

	case advisory != nil:
		return Assessment{StateAdvisory, "whenUnsatisfiable is ScheduleAnyway, which gives no guarantee under pressure"}

	default:
		return Assessment{StateAbsent, "no topology spread constraint for this domain key"}
	}
}

// LiveSkew computes the actual skew of current placement: the largest count in
// any domain minus the smallest, counting domains with no pods as zero.
//
// Constraints bind only at scheduling time, so live skew can exceed maxSkew
// after node loss or a scale-down (ReplicaSet deletion ranking is not
// zone-aware, upstream #124306).
func LiveSkew(placement map[string]int, domains []string) int {
	if len(domains) == 0 {
		return 0
	}
	max, min := 0, -1
	for _, d := range domains {
		n := placement[d]
		if n > max {
			max = n
		}
		if min == -1 || n < min {
			min = n
		}
	}
	if min == -1 {
		min = 0
	}
	return max - min
}

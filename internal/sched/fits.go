package sched

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	schedframework "k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
)

// Fit is one node's answer for one candidate pod.
type Fit struct {
	Node   string
	Domain string
	OK     bool
	Reason string // the plugin's own message when OK is false
}

// Fits reports, for every node in the snapshot, whether the real scheduler's
// Filter plugins would accept this pod there.
//
// The pod must be unassigned: an assigned pod is already counted in the
// snapshot and would be weighed against itself.
func (s *Scheduler) Fits(ctx context.Context, pod *corev1.Pod, domainKey string) ([]Fit, error) {
	if pod.Spec.NodeName != "" {
		return nil, fmt.Errorf("Fits needs an unassigned pod; %s/%s is already on %s",
			pod.Namespace, pod.Name, pod.Spec.NodeName)
	}

	state := schedframework.NewCycleState()
	_, status, _ := s.framework.RunPreFilterPlugins(ctx, state, pod)
	if !status.IsSuccess() {
		// PreFilter rejects the pod, not a node. Returning "nothing fits" would
		// be read as a capacity problem, which is a different and fixable thing.
		return nil, fmt.Errorf("pod rejected before filtering: %s", status.Message())
	}

	out := make([]Fit, 0, len(s.nodes))
	for _, n := range s.nodes {
		ni, err := s.snapshot.NodeInfos().Get(n.Name)
		if err != nil {
			return nil, fmt.Errorf("node info for %s: %w", n.Name, err)
		}
		d, _ := domain.Of(n, domainKey)
		f := Fit{Node: n.Name, Domain: d, OK: true}
		if st := s.framework.RunFilterPlugins(ctx, state, pod, ni); !st.IsSuccess() {
			f.OK = false
			f.Reason = st.Message()
		}
		out = append(out, f)
	}
	return out, nil
}

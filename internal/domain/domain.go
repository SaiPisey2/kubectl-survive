// Package domain resolves which failure domain a node belongs to.
package domain

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
)

const (
	// LabelZone is the current well-known zone label.
	LabelZone = "topology.kubernetes.io/zone"
	// LabelZoneDeprecated predates LabelZone. Long-lived clusters and
	// PersistentVolumes provisioned years ago may carry only this one.
	LabelZoneDeprecated = "failure-domain.beta.kubernetes.io/zone"
)

// Of returns the node's domain for the given label key.
//
// The deprecated zone label is consulted only when the caller asked for the
// stable zone label; a custom key gets no fallback because we cannot know an
// equivalent. An empty value counts as absent: a domain named "" would
// silently merge unrelated nodes.
func Of(node *corev1.Node, key string) (string, bool) {
	if v, ok := node.Labels[key]; ok && v != "" {
		return v, true
	}
	if key == LabelZone {
		if v, ok := node.Labels[LabelZoneDeprecated]; ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// Group buckets nodes by domain. Nodes with no resolvable domain are returned
// separately rather than dropped: a node we cannot place is a hole in the
// analysis, not an irrelevance.
func Group(nodes []*corev1.Node, key string) (map[string][]string, []string) {
	groups := map[string][]string{}
	var unlabelled []string
	for _, n := range nodes {
		if d, ok := Of(n, key); ok {
			groups[d] = append(groups[d], n.Name)
		} else {
			unlabelled = append(unlabelled, n.Name)
		}
	}
	for d := range groups {
		sort.Strings(groups[d])
	}
	sort.Strings(unlabelled)
	return groups, unlabelled
}

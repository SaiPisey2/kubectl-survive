// Package depgraph builds the workload dependency graph that survivability
// is actually a property of (spec §5.5): "a service spread perfectly across
// three zones is still dead if the database it calls has its only primary in
// the zone that vanished."
//
// This package is pure -- no scheduler, no cluster -- and takes only a
// *snapshot.Snapshot, which is what lets Build's edges be tested without a
// live cluster and what keeps it in the Makefile's CORE dependency-guard
// list alongside the rest of the survivability core.
//
// Edges are asserted only from facts already in the cluster and only where
// they can be proven (spec §5.5, ruling 3): Service->workload edges come
// from EndpointSlice targetRefs, never from Service selectors (selectors are
// intent, EndpointSlices are truth); workload->Service edges come only from
// literal env var `value` strings (spec §11: the tool reads no Secrets and
// no ConfigMap contents, so valueFrom, envFrom and mounted volumes are never
// followed). Where an edge cannot be proven it is not asserted.
package depgraph

import (
	"sort"

	"github.com/SaiPisey2/kubectl-survive/internal/workload"
)

// ServiceKey identifies a Service by namespace and name.
type ServiceKey struct {
	Namespace string
	Name      string
}

func (k ServiceKey) String() string { return k.Namespace + "/" + k.Name }

// Edge is one workload's dependency on a Service, resolved (where possible)
// to the workload(s) actually backing it.
type Edge struct {
	From    workload.Ref
	Service ServiceKey

	// Backers is who EndpointSlice truth says actually serves this Service,
	// resolved to their owning workload. Empty when Unresolved.
	Backers []workload.Ref

	// Unresolved is true when the Service has no resolvable endpoints. Per
	// spec §5.5, this is recorded, never asserted through: the dependency is
	// real but its target is unknown, not "no dependency".
	Unresolved bool
}

// Graph is the dependency edge set, built once from a Snapshot. It holds no
// domain-specific state; the same Graph is reused to answer every domain's
// propagation query.
type Graph struct {
	Edges  []Edge
	byFrom map[workload.Ref][]Edge
}

// DependsOn returns ref's direct dependencies as display strings, for the
// report's "dependsOn" field (spec §8.4): resolved backers by "<ns>/<name>",
// unresolved Services named and marked explicitly.
func (g *Graph) DependsOn(ref workload.Ref) []string {
	var out []string
	for _, e := range g.byFrom[ref] {
		if e.Unresolved {
			out = append(out, e.Service.String()+" (unresolved service)")
			continue
		}
		for _, b := range e.Backers {
			out = append(out, b.String())
		}
	}
	sort.Strings(out)
	return out
}

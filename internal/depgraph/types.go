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

// EdgeKind classifies why an Edge does or doesn't carry real Backers, as an
// explicit discriminator rather than a combination of booleans -- so the
// three cases below can never blur into each other again (this replaced a
// single Unresolved bool that conflated all three: a genuinely suspicious
// unresolved Service was treated identically to a Service that can *never*
// resolve to a workload, which meant a workload calling RDS through an
// ExternalName Service was reported impaired, in every zone, permanently).
type EdgeKind int

const (
	// EdgeResolved is a normal edge: the Service (selector-backed or
	// selectorless) has at least one endpoint whose Pod targetRef resolves
	// to an owning workload, recorded in Backers.
	EdgeResolved EdgeKind = iota

	// EdgeUnresolved is a selector-backed Service with no resolvable
	// endpoints -- genuinely suspicious (scaled to zero, crashlooping, or
	// mislabelled) -- so it is recorded, never asserted through, and it
	// propagates impairment (spec §5.5 ruling 3).
	EdgeUnresolved

	// EdgeExternal is an ExternalName Service: it points outside the
	// cluster by definition (spec §5.6, cluster-external dependencies are
	// invisible) and can never resolve to a workload. The edge is recorded
	// so DependsOn shows it, marked "(external)", but it never propagates
	// impairment: a false edge is worse than a missing one.
	EdgeExternal

	// EdgeUnattributable is a selectorless Service (any type other than
	// ExternalName, with spec.selector nil or empty) whose EndpointSlices
	// carry no Pod targetRef that resolves to a workload. Its endpoints are
	// managed out of band -- often pointing outside the cluster -- so the
	// tool cannot attribute them to a zone. Recorded, marked
	// "(not attributable)", never impairing. A selectorless Service whose
	// EndpointSlices DO carry a resolvable Pod targetRef is EdgeResolved
	// instead, because then the backer can be proven.
	EdgeUnattributable
)

// Edge is one workload's dependency on a Service, resolved (where possible)
// to the workload(s) actually backing it.
type Edge struct {
	From    workload.Ref
	Service ServiceKey

	// Backers is who EndpointSlice truth says actually serves this Service,
	// resolved to their owning workload. Empty unless Kind is EdgeResolved.
	Backers []workload.Ref

	// Kind says why this edge does or doesn't have real Backers. See
	// EdgeKind.
	Kind EdgeKind
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
// unresolved, external and not-attributable Services named and marked
// explicitly.
func (g *Graph) DependsOn(ref workload.Ref) []string {
	var out []string
	for _, e := range g.byFrom[ref] {
		switch e.Kind {
		case EdgeUnresolved:
			out = append(out, e.Service.String()+" (unresolved service)")
		case EdgeExternal:
			out = append(out, e.Service.String()+" (external)")
		case EdgeUnattributable:
			out = append(out, e.Service.String()+" (not attributable)")
		default:
			for _, b := range e.Backers {
				out = append(out, b.String())
			}
		}
	}
	sort.Strings(out)
	return out
}

package depgraph

import (
	"sort"

	"github.com/SaiPisey2/kubectl-survive/internal/workload"
)

// Impairment records that a workload, while not itself lost in a domain, is
// impaired there because something it transitively depends on is.
type Impairment struct {
	Workload workload.Ref
	Domain   string
	// Chain names the path from Workload to the failing dependency, e.g.
	// []string{"web", "session-store"}, so the operator sees why.
	Chain []string
}

// Impaired reports every workload that is impaired by a dependency in
// domain: not itself lost there, but transitively depending -- through one
// or more resolved or unresolved Services -- on a workload that is lost or
// unknown there (an unresolved Service counts the same as unknown: "unknown
// is never reported as safe").
//
// lostOrUnknown marks refs whose own-pod outcome in this domain is itself
// Lost or Unknown -- the signal that a dependency has actually failed.
// ownLost marks refs whose own-pod outcome is Lost: these are excluded from
// the result entirely, because a workload already reported Lost gains
// nothing from also being reported Impaired (ruling 1).
//
// Cycles are broken with a per-workload visited set.
func Impaired(g *Graph, domain string, lostOrUnknown, ownLost map[workload.Ref]bool) []Impairment {
	var refs []workload.Ref
	for ref := range g.byFrom {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })

	var out []Impairment
	for _, ref := range refs {
		if ownLost[ref] {
			continue
		}
		if found, chain := dfs(g, ref, lostOrUnknown, map[workload.Ref]bool{}, nil); found {
			out = append(out, Impairment{Workload: ref, Domain: domain, Chain: chain})
		}
	}
	return out
}

// dfs walks the graph from ref looking for the first proof that ref
// transitively depends on something lost, unknown, or unresolved. It
// returns the chain of workload (and, for the terminal unresolved case,
// Service) names leading from ref to that proof.
func dfs(g *Graph, ref workload.Ref, lostOrUnknown, visited map[workload.Ref]bool, path []string) (bool, []string) {
	if visited[ref] {
		return false, nil
	}
	visited[ref] = true
	cur := append(append([]string{}, path...), ref.Name)

	for _, e := range g.byFrom[ref] {
		if e.Unresolved {
			return true, append(append([]string{}, cur...), e.Service.String()+" (unresolved service)")
		}
		for _, backer := range e.Backers {
			if lostOrUnknown[backer] {
				return true, append(append([]string{}, cur...), backer.Name)
			}
		}
		for _, backer := range e.Backers {
			if found, chain := dfs(g, backer, lostOrUnknown, visited, cur); found {
				return true, chain
			}
		}
	}
	return false, nil
}

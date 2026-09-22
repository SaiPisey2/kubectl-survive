package depgraph

import (
	"reflect"
	"testing"

	"github.com/SaiPisey2/kubectl-survive/internal/workload"
)

func graphOf(edges ...Edge) *Graph {
	g := &Graph{byFrom: map[workload.Ref][]Edge{}}
	for _, e := range edges {
		g.Edges = append(g.Edges, e)
		g.byFrom[e.From] = append(g.byFrom[e.From], e)
	}
	return g
}

func ref(kind, name string) workload.Ref {
	return workload.Ref{Kind: kind, Namespace: "default", Name: name}
}

// TestImpairedFindsTransitiveLostDependency is the direct example from the
// spec: web is fine in this domain but session-store, which it depends on,
// is lost there.
func TestImpairedFindsTransitiveLostDependency(t *testing.T) {
	web := ref("Deployment", "web")
	sessionStore := ref("StatefulSet", "session-store")

	g := graphOf(Edge{
		From:    web,
		Service: ServiceKey{Namespace: "default", Name: "session-store"},
		Backers: []workload.Ref{sessionStore},
	})

	lostOrUnknown := map[workload.Ref]bool{sessionStore: true}
	got := Impaired(g, "us-east-1a", lostOrUnknown, nil)

	if len(got) != 1 {
		t.Fatalf("got %d impairments, want 1: %+v", len(got), got)
	}
	if got[0].Workload != web {
		t.Errorf("Workload = %v, want %v", got[0].Workload, web)
	}
	want := []string{"web", "session-store"}
	if !reflect.DeepEqual(got[0].Chain, want) {
		t.Errorf("Chain = %v, want %v", got[0].Chain, want)
	}
}

// TestImpairedTreatsUnknownDependencyAsImpairing: "unknown is never
// reported as safe."
func TestImpairedTreatsUnknownDependencyAsImpairing(t *testing.T) {
	web := ref("Deployment", "web")
	backend := ref("Deployment", "backend")

	g := graphOf(Edge{
		From:    web,
		Service: ServiceKey{Namespace: "default", Name: "backend"},
		Backers: []workload.Ref{backend},
	})

	// backend's own outcome is Unknown in this domain, not Lost.
	lostOrUnknown := map[workload.Ref]bool{backend: true}
	got := Impaired(g, "us-east-1a", lostOrUnknown, nil)
	if len(got) != 1 {
		t.Fatalf("got %d impairments, want 1 (unknown must impair)", len(got))
	}
}

// TestImpairedTreatsUnresolvedServiceAsImpairing: an unresolved Service must
// never read as "no dependency".
func TestImpairedTreatsUnresolvedServiceAsImpairing(t *testing.T) {
	web := ref("Deployment", "web")

	g := graphOf(Edge{
		From:    web,
		Service: ServiceKey{Namespace: "default", Name: "ghost"},
		Kind:    EdgeUnresolved,
	})

	got := Impaired(g, "us-east-1a", nil, nil)
	if len(got) != 1 {
		t.Fatalf("got %d impairments, want 1", len(got))
	}
	want := []string{"web", "default/ghost (unresolved service)"}
	if !reflect.DeepEqual(got[0].Chain, want) {
		t.Errorf("Chain = %v, want %v", got[0].Chain, want)
	}
}

// TestImpairedNeverPropagatesThroughExternalNameService proves ruling 1: an
// ExternalName Service (e.g. an RDS/Cloud SQL address wired through a
// Service of that type) is never impairing, in any domain, no matter what
// lostOrUnknown says -- it's a false edge if propagated, and a false edge is
// worse than a missing one.
func TestImpairedNeverPropagatesThroughExternalNameService(t *testing.T) {
	web := ref("Deployment", "web")

	g := graphOf(Edge{
		From:    web,
		Service: ServiceKey{Namespace: "default", Name: "db"},
		Kind:    EdgeExternal,
	})

	got := Impaired(g, "us-east-1a", nil, nil)
	if len(got) != 0 {
		t.Fatalf("got %d impairments, want 0 (ExternalName never impairs): %+v", len(got), got)
	}
}

// TestImpairedNeverPropagatesThroughUnattributableService proves ruling 2:
// a selectorless Service with no resolvable Pod targetRef is never
// impairing.
func TestImpairedNeverPropagatesThroughUnattributableService(t *testing.T) {
	web := ref("Deployment", "web")

	g := graphOf(Edge{
		From:    web,
		Service: ServiceKey{Namespace: "default", Name: "cache"},
		Kind:    EdgeUnattributable,
	})

	got := Impaired(g, "us-east-1a", nil, nil)
	if len(got) != 0 {
		t.Fatalf("got %d impairments, want 0 (unattributable Service never impairs): %+v", len(got), got)
	}
}

// TestImpairedPropagatesThroughResolvedSelectorlessService proves that once
// a selectorless Service's EndpointSlice DOES resolve to a real workload
// (EdgeResolved), impairment propagates through it exactly like any other
// resolved dependency.
func TestImpairedPropagatesThroughResolvedSelectorlessService(t *testing.T) {
	web := ref("Deployment", "web")
	cache := ref("StatefulSet", "cache")

	g := graphOf(Edge{
		From:    web,
		Service: ServiceKey{Namespace: "default", Name: "cache"},
		Backers: []workload.Ref{cache},
		Kind:    EdgeResolved,
	})

	lostOrUnknown := map[workload.Ref]bool{cache: true}
	got := Impaired(g, "us-east-1a", lostOrUnknown, nil)
	if len(got) != 1 {
		t.Fatalf("got %d impairments, want 1 (resolved selectorless dependency must still impair)", len(got))
	}
}

// TestImpairedExcludesWorkloadsAlreadyLost: ruling 1 -- a workload whose own
// pods are lost is not also reported Impaired.
func TestImpairedExcludesWorkloadsAlreadyLost(t *testing.T) {
	web := ref("Deployment", "web")
	backend := ref("Deployment", "backend")

	g := graphOf(Edge{
		From:    web,
		Service: ServiceKey{Namespace: "default", Name: "backend"},
		Backers: []workload.Ref{backend},
	})

	lostOrUnknown := map[workload.Ref]bool{backend: true}
	ownLost := map[workload.Ref]bool{web: true}
	got := Impaired(g, "us-east-1a", lostOrUnknown, ownLost)
	if len(got) != 0 {
		t.Fatalf("got %d impairments, want 0 (already lost, excluded)", len(got))
	}
}

// TestImpairedFollowsTransitivelyThroughASurvivingHop: web -> gateway ->
// db, where gateway itself survives but db is lost. web must still be
// reported impaired, with the full chain.
func TestImpairedFollowsTransitivelyThroughASurvivingHop(t *testing.T) {
	web := ref("Deployment", "web")
	gateway := ref("Deployment", "gateway")
	db := ref("StatefulSet", "db")

	g := graphOf(
		Edge{From: web, Service: ServiceKey{Namespace: "default", Name: "gateway"}, Backers: []workload.Ref{gateway}},
		Edge{From: gateway, Service: ServiceKey{Namespace: "default", Name: "db"}, Backers: []workload.Ref{db}},
	)

	lostOrUnknown := map[workload.Ref]bool{db: true}
	got := Impaired(g, "us-east-1a", lostOrUnknown, nil)

	byWorkload := map[workload.Ref]Impairment{}
	for _, imp := range got {
		byWorkload[imp.Workload] = imp
	}
	if _, ok := byWorkload[web]; !ok {
		t.Fatalf("web not reported impaired: %+v", got)
	}
	want := []string{"web", "gateway", "db"}
	if !reflect.DeepEqual(byWorkload[web].Chain, want) {
		t.Errorf("Chain = %v, want %v", byWorkload[web].Chain, want)
	}
	if _, ok := byWorkload[gateway]; !ok {
		t.Errorf("gateway not reported impaired: %+v", got)
	}
}

// TestImpairedBreaksCycles proves the visited set stops infinite recursion
// on a dependency cycle that never actually resolves to anything lost.
func TestImpairedBreaksCycles(t *testing.T) {
	a := ref("Deployment", "a")
	b := ref("Deployment", "b")

	g := graphOf(
		Edge{From: a, Service: ServiceKey{Namespace: "default", Name: "b"}, Backers: []workload.Ref{b}},
		Edge{From: b, Service: ServiceKey{Namespace: "default", Name: "a"}, Backers: []workload.Ref{a}},
	)

	got := Impaired(g, "us-east-1a", nil, nil)
	if len(got) != 0 {
		t.Fatalf("got %d impairments on a cycle with nothing lost, want 0: %+v", len(got), got)
	}
}

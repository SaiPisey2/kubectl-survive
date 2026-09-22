package depgraph

import (
	"sort"

	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
)

// Build constructs the dependency edge set from a Snapshot: which workloads
// actually back which Services (from EndpointSlice targetRefs, resolved
// through idx.Owner -- Service selectors are only intent, EndpointSlices are
// the truth about who serves), and which workloads declare a dependency on a
// Service by naming it in a literal container env var value.
func Build(s *snapshot.Snapshot, idx *workload.Index) *Graph {
	svcByKey := map[ServiceKey]*corev1.Service{}
	for _, svc := range s.Services {
		svcByKey[ServiceKey{Namespace: svc.Namespace, Name: svc.Name}] = svc
	}
	exists := func(ns, name string) bool {
		_, ok := svcByKey[ServiceKey{Namespace: ns, Name: name}]
		return ok
	}

	podByKey := map[string]*corev1.Pod{}
	for _, p := range s.Pods {
		podByKey[p.Namespace+"/"+p.Name] = p
	}

	backers := map[ServiceKey]map[workload.Ref]bool{}
	for _, es := range s.EndpointSlices {
		svcName := es.Labels[discoveryv1.LabelServiceName]
		if svcName == "" {
			continue
		}
		key := ServiceKey{Namespace: es.Namespace, Name: svcName}
		for _, ep := range es.Endpoints {
			if ep.TargetRef == nil || ep.TargetRef.Kind != "Pod" {
				continue
			}
			pod, ok := podByKey[ep.TargetRef.Namespace+"/"+ep.TargetRef.Name]
			if !ok {
				continue
			}
			ref, ok := idx.Owner(pod)
			if !ok {
				ref = workload.Ref{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
			}
			if backers[key] == nil {
				backers[key] = map[workload.Ref]bool{}
			}
			backers[key][ref] = true
		}
		// A Service that has at least one EndpointSlice but zero usable
		// endpoints (or none whose targetRef resolves) is still known to
		// exist and is still unresolved: recorded below via the presence
		// check against backers[key], not skipped.
		if _, ok := backers[key]; !ok {
			backers[key] = map[workload.Ref]bool{}
		}
	}

	type edgeKey struct {
		From workload.Ref
		Svc  ServiceKey
	}
	seen := map[edgeKey]bool{}

	g := &Graph{byFrom: map[workload.Ref][]Edge{}}
	for _, pod := range s.Pods {
		ref, ok := idx.Owner(pod)
		if !ok {
			ref = workload.Ref{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
		}
		for _, c := range allContainers(pod) {
			for _, ev := range c.Env {
				if ev.Value == "" {
					// ValueFrom (Secret/ConfigMap/field/resource refs) is
					// never followed: spec §11, the tool reads no Secrets
					// and no ConfigMap contents.
					continue
				}
				key, matched := matchServiceRef(ev.Value, pod.Namespace, exists)
				if !matched {
					continue
				}
				ek := edgeKey{From: ref, Svc: key}
				if seen[ek] {
					continue
				}
				seen[ek] = true

				var refs []workload.Ref
				for r := range backers[key] {
					refs = append(refs, r)
				}
				sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })

				kind := classifyEdge(svcByKey[key], refs)
				if kind == EdgeExternal {
					// An ExternalName Service points outside the cluster by
					// definition; it never resolves to a workload, so any
					// EndpointSlice-derived refs (there normally are none)
					// are dropped rather than asserted as Backers.
					refs = nil
				}

				e := Edge{From: ref, Service: key, Backers: refs, Kind: kind}
				g.Edges = append(g.Edges, e)
				g.byFrom[ref] = append(g.byFrom[ref], e)
			}
		}
	}

	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From.String() != g.Edges[j].From.String() {
			return g.Edges[i].From.String() < g.Edges[j].From.String()
		}
		return g.Edges[i].Service.String() < g.Edges[j].Service.String()
	})
	for ref := range g.byFrom {
		edges := g.byFrom[ref]
		sort.Slice(edges, func(i, j int) bool { return edges[i].Service.String() < edges[j].Service.String() })
		g.byFrom[ref] = edges
	}

	return g
}

// classifyEdge decides an Edge's EdgeKind from the matched Service's own
// spec and the endpoint refs already resolved for it (see the ruling in
// EdgeKind's doc comment):
//
//   - ExternalName Services point outside the cluster and can never resolve
//     to a workload: always EdgeExternal, regardless of refs.
//   - Selectorless Services (any other type, nil/empty spec.Selector) have
//     endpoints managed out of band. If EndpointSlice truth still resolved
//     a Pod targetRef to a workload, that's proof: EdgeResolved. Otherwise
//     the tool cannot attribute the Service to a zone: EdgeUnattributable.
//   - Everything else is a normal selector-backed Service: EdgeResolved
//     when it has resolvable endpoints, EdgeUnresolved (and impairing)
//     when it doesn't.
//
// svc is nil only if the matched key isn't in the snapshot's Services,
// which matchServiceRef's exists() check already rules out; treated as a
// normal selector-backed Service defensively.
func classifyEdge(svc *corev1.Service, refs []workload.Ref) EdgeKind {
	switch {
	case svc != nil && svc.Spec.Type == corev1.ServiceTypeExternalName:
		return EdgeExternal
	case svc != nil && len(svc.Spec.Selector) == 0:
		if len(refs) == 0 {
			return EdgeUnattributable
		}
		return EdgeResolved
	default:
		if len(refs) == 0 {
			return EdgeUnresolved
		}
		return EdgeResolved
	}
}

func allContainers(pod *corev1.Pod) []corev1.Container {
	out := make([]corev1.Container, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
	out = append(out, pod.Spec.InitContainers...)
	out = append(out, pod.Spec.Containers...)
	return out
}

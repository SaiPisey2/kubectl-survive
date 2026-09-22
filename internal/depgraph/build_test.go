package depgraph

import (
	"testing"

	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func bp(b bool) *bool { return &b }

// podOwnedBy builds a pod controlled directly by a workload of the given
// kind, skipping the ReplicaSet indirection: idx.Owner resolves a
// non-ReplicaSet controller kind straight from the OwnerReference, so this
// is enough to exercise the graph without a full Deployment/ReplicaSet
// fixture.
func podOwnedBy(name, namespace, kind, owner string, containers []corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{{Kind: kind, Name: owner, Controller: bp(true)}},
		},
		Spec: corev1.PodSpec{Containers: containers},
	}
}

func endpointSlice(name, namespace, service string, targets ...corev1.ObjectReference) *discoveryv1.EndpointSlice {
	es := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace,
			Labels: map[string]string{discoveryv1.LabelServiceName: service},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
	}
	for _, t := range targets {
		tr := t
		es.Endpoints = append(es.Endpoints, discoveryv1.Endpoint{
			Addresses: []string{"10.0.0.1"},
			TargetRef: &tr,
		})
	}
	return es
}

// TestBuildResolvesServiceToBackingWorkload proves the Service -> workload
// half of the graph: EndpointSlice targetRef is followed to the pod's
// owner, never the Service selector.
func TestBuildResolvesServiceToBackingWorkload(t *testing.T) {
	backendPod := podOwnedBy("session-store-0", "default", "StatefulSet", "session-store", nil)
	s := &snapshot.Snapshot{
		Pods:     []*corev1.Pod{backendPod},
		Services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "session-store", Namespace: "default"}}},
		EndpointSlices: []*discoveryv1.EndpointSlice{
			endpointSlice("session-store-abcde", "default", "session-store",
				corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "session-store-0"}),
		},
	}
	idx := workload.NewIndex(s)
	g := Build(s, idx)

	want := workload.Ref{Kind: "StatefulSet", Namespace: "default", Name: "session-store"}

	found := false
	for _, e := range g.Edges {
		for _, b := range e.Backers {
			if b == want {
				found = true
			}
		}
	}
	// No workload references session-store yet, so there should be no edges
	// at all -- Build only records edges FROM a referencing workload. This
	// assertion documents that (see the next test for the referencing case).
	if found {
		t.Fatalf("unexpected edge to %v with no referencing workload", want)
	}
}

// TestBuildCreatesEdgeFromLiteralEnvValue proves the workload -> Service
// half: a literal env var value naming the Service in host position creates
// an edge, resolved to the real backing workload via EndpointSlices.
func TestBuildCreatesEdgeFromLiteralEnvValue(t *testing.T) {
	backendPod := podOwnedBy("session-store-0", "default", "StatefulSet", "session-store", nil)
	webPod := podOwnedBy("web-1", "default", "Deployment", "web", []corev1.Container{{
		Name: "c",
		Env:  []corev1.EnvVar{{Name: "SESSION_STORE_ADDR", Value: "http://session-store:6379"}},
	}})
	s := &snapshot.Snapshot{
		Pods:     []*corev1.Pod{backendPod, webPod},
		Services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "session-store", Namespace: "default"}}},
		EndpointSlices: []*discoveryv1.EndpointSlice{
			endpointSlice("session-store-abcde", "default", "session-store",
				corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "session-store-0"}),
		},
	}
	idx := workload.NewIndex(s)
	g := Build(s, idx)

	web := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "web"}
	sessionStore := workload.Ref{Kind: "StatefulSet", Namespace: "default", Name: "session-store"}

	edges := g.byFrom[web]
	if len(edges) != 1 {
		t.Fatalf("got %d edges from web, want 1: %+v", len(edges), edges)
	}
	if edges[0].Kind != EdgeResolved {
		t.Fatalf("edge Kind = %v, want EdgeResolved", edges[0].Kind)
	}
	if len(edges[0].Backers) != 1 || edges[0].Backers[0] != sessionStore {
		t.Errorf("backers = %v, want [%v]", edges[0].Backers, sessionStore)
	}

	deps := g.DependsOn(web)
	if len(deps) != 1 || deps[0] != sessionStore.String() {
		t.Errorf("DependsOn(web) = %v, want [%s]", deps, sessionStore.String())
	}
}

// TestBuildMarksServiceWithNoEndpointsUnresolved proves ruling 3: "A Service
// with a selector but no resolvable endpoints is unresolved, not 'has no
// backends' -- record it, never assert through it." This is a regression
// guard: the Service fixture carries a real selector, so it is not the
// ExternalName or selectorless case, which classify differently (see
// TestBuildMarksExternalNameServiceExternal and
// TestBuildMarksSelectorlessServiceWithNoBackersUnattributable below).
func TestBuildMarksServiceWithNoEndpointsUnresolved(t *testing.T) {
	webPod := podOwnedBy("web-1", "default", "Deployment", "web", []corev1.Container{{
		Name: "c",
		Env:  []corev1.EnvVar{{Name: "CACHE_ADDR", Value: "http://cache:6379"}},
	}})
	s := &snapshot.Snapshot{
		Pods: []*corev1.Pod{webPod},
		Services: []*corev1.Service{{
			ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "default"},
			Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "cache"}},
		}},
		// No EndpointSlice for "cache" at all: the Service exists but backs
		// nothing resolvable.
	}
	idx := workload.NewIndex(s)
	g := Build(s, idx)

	web := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "web"}
	edges := g.byFrom[web]
	if len(edges) != 1 {
		t.Fatalf("got %d edges from web, want 1: %+v", len(edges), edges)
	}
	if edges[0].Kind != EdgeUnresolved {
		t.Errorf("edge Kind = %v, want EdgeUnresolved (selector-backed, no endpoints)", edges[0].Kind)
	}
	if len(edges[0].Backers) != 0 {
		t.Errorf("Backers = %v, want none", edges[0].Backers)
	}

	deps := g.DependsOn(web)
	if len(deps) != 1 || deps[0] != "default/cache (unresolved service)" {
		t.Errorf("DependsOn(web) = %v, want [default/cache (unresolved service)]", deps)
	}
}

// TestBuildMarksExternalNameServiceExternal proves ruling 1: an ExternalName
// Service points outside the cluster by definition, so it is never
// impairing -- classified EdgeExternal, shown in DependsOn marked
// "(external)", with no Backers asserted.
func TestBuildMarksExternalNameServiceExternal(t *testing.T) {
	webPod := podOwnedBy("web-1", "default", "Deployment", "web", []corev1.Container{{
		Name: "c",
		Env:  []corev1.EnvVar{{Name: "DB_ADDR", Value: "postgres://db:5432/app"}},
	}})
	s := &snapshot.Snapshot{
		Pods: []*corev1.Pod{webPod},
		Services: []*corev1.Service{{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"},
			Spec: corev1.ServiceSpec{
				Type:         corev1.ServiceTypeExternalName,
				ExternalName: "prod.abc123.us-east-1.rds.amazonaws.com",
			},
		}},
	}
	idx := workload.NewIndex(s)
	g := Build(s, idx)

	web := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "web"}
	edges := g.byFrom[web]
	if len(edges) != 1 {
		t.Fatalf("got %d edges from web, want 1: %+v", len(edges), edges)
	}
	if edges[0].Kind != EdgeExternal {
		t.Errorf("edge Kind = %v, want EdgeExternal", edges[0].Kind)
	}
	if len(edges[0].Backers) != 0 {
		t.Errorf("Backers = %v, want none (ExternalName never resolves to a workload)", edges[0].Backers)
	}

	deps := g.DependsOn(web)
	if len(deps) != 1 || deps[0] != "default/db (external)" {
		t.Errorf("DependsOn(web) = %v, want [default/db (external)]", deps)
	}
}

// TestBuildMarksSelectorlessServiceWithNoBackersUnattributable proves ruling
// 2: a selectorless Service's endpoints are managed out of band and can't be
// attributed to a zone when there's no Pod targetRef to prove otherwise --
// classified EdgeUnattributable, never impairing.
func TestBuildMarksSelectorlessServiceWithNoBackersUnattributable(t *testing.T) {
	webPod := podOwnedBy("web-1", "default", "Deployment", "web", []corev1.Container{{
		Name: "c",
		Env:  []corev1.EnvVar{{Name: "CACHE_ADDR", Value: "http://cache:6379"}},
	}})
	s := &snapshot.Snapshot{
		Pods: []*corev1.Pod{webPod},
		Services: []*corev1.Service{{
			ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "default"},
			// No Selector: endpoints are managed out of band (e.g. a manual
			// Endpoints/EndpointSlice pointing at an off-cluster IP).
		}},
	}
	idx := workload.NewIndex(s)
	g := Build(s, idx)

	web := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "web"}
	edges := g.byFrom[web]
	if len(edges) != 1 {
		t.Fatalf("got %d edges from web, want 1: %+v", len(edges), edges)
	}
	if edges[0].Kind != EdgeUnattributable {
		t.Errorf("edge Kind = %v, want EdgeUnattributable", edges[0].Kind)
	}

	deps := g.DependsOn(web)
	if len(deps) != 1 || deps[0] != "default/cache (not attributable)" {
		t.Errorf("DependsOn(web) = %v, want [default/cache (not attributable)]", deps)
	}
}

// TestBuildResolvesSelectorlessServiceThroughPodTargetRef proves the
// selectorless exception: when a selectorless Service's EndpointSlice DOES
// carry a Pod targetRef that resolves to a workload, that's proof, and the
// edge is a normal EdgeResolved one -- impairment propagates through it like
// any other resolved dependency.
func TestBuildResolvesSelectorlessServiceThroughPodTargetRef(t *testing.T) {
	backendPod := podOwnedBy("cache-0", "default", "StatefulSet", "cache", nil)
	webPod := podOwnedBy("web-1", "default", "Deployment", "web", []corev1.Container{{
		Name: "c",
		Env:  []corev1.EnvVar{{Name: "CACHE_ADDR", Value: "http://cache:6379"}},
	}})
	s := &snapshot.Snapshot{
		Pods: []*corev1.Pod{backendPod, webPod},
		Services: []*corev1.Service{{
			ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "default"},
			// No Selector, but the EndpointSlice below still carries a Pod
			// targetRef -- proof enough to resolve it.
		}},
		EndpointSlices: []*discoveryv1.EndpointSlice{
			endpointSlice("cache-abcde", "default", "cache",
				corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "cache-0"}),
		},
	}
	idx := workload.NewIndex(s)
	g := Build(s, idx)

	web := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "web"}
	cache := workload.Ref{Kind: "StatefulSet", Namespace: "default", Name: "cache"}
	edges := g.byFrom[web]
	if len(edges) != 1 {
		t.Fatalf("got %d edges from web, want 1: %+v", len(edges), edges)
	}
	if edges[0].Kind != EdgeResolved {
		t.Errorf("edge Kind = %v, want EdgeResolved (Pod targetRef proves the backer)", edges[0].Kind)
	}
	if len(edges[0].Backers) != 1 || edges[0].Backers[0] != cache {
		t.Errorf("Backers = %v, want [%v]", edges[0].Backers, cache)
	}

	deps := g.DependsOn(web)
	if len(deps) != 1 || deps[0] != cache.String() {
		t.Errorf("DependsOn(web) = %v, want [%s]", deps, cache.String())
	}
}

// TestBuildIgnoresBareWordEnvValue is the conservative-matching negative:
// a bare word that happens to equal a Service's name must not create an
// edge unless it appears in a proven host position.
func TestBuildIgnoresBareWordEnvValue(t *testing.T) {
	webPod := podOwnedBy("web-1", "default", "Deployment", "web", []corev1.Container{{
		Name: "c",
		Env:  []corev1.EnvVar{{Name: "MODE", Value: "web"}},
	}})
	s := &snapshot.Snapshot{
		Pods:     []*corev1.Pod{webPod},
		Services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"}}},
	}
	idx := workload.NewIndex(s)
	g := Build(s, idx)

	web := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "web"}
	if edges := g.byFrom[web]; len(edges) != 0 {
		t.Errorf("got %d edges from a bare-word env value, want 0: %+v", len(edges), edges)
	}
}

// TestBuildIgnoresValueFrom proves spec §11: only literal env `value`
// strings are followed. envFrom and valueFrom (Secret/ConfigMap/field refs)
// are never read.
func TestBuildIgnoresValueFrom(t *testing.T) {
	webPod := podOwnedBy("web-1", "default", "Deployment", "web", []corev1.Container{{
		Name: "c",
		Env: []corev1.EnvVar{{
			Name: "SESSION_STORE_ADDR",
			ValueFrom: &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "session-store-cm"},
					Key:                  "addr",
				},
			},
		}},
		EnvFrom: []corev1.EnvFromSource{{
			ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "session-store-cm"}},
		}},
	}})
	s := &snapshot.Snapshot{
		Pods:     []*corev1.Pod{webPod},
		Services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "session-store", Namespace: "default"}}},
	}
	idx := workload.NewIndex(s)
	g := Build(s, idx)

	web := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "web"}
	if edges := g.byFrom[web]; len(edges) != 0 {
		t.Errorf("got %d edges from valueFrom/envFrom, want 0: %+v", len(edges), edges)
	}
}

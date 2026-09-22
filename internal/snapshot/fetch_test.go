// internal/snapshot/fetch_test.go
package snapshot

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestFetchCollectsEverything(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "default"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc1", Namespace: "default"}},
		&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "svc1-abcde", Namespace: "default"}},
	)
	snap, err := Fetch(context.Background(), cs)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(snap.Nodes) != 1 || len(snap.Pods) != 1 {
		t.Errorf("got %d nodes %d pods, want 1 and 1", len(snap.Nodes), len(snap.Pods))
	}
	if len(snap.Services) != 1 {
		t.Errorf("got %d services, want 1", len(snap.Services))
	}
	if len(snap.EndpointSlices) != 1 {
		t.Errorf("got %d endpointslices, want 1", len(snap.EndpointSlices))
	}
	if snap.TakenAt.IsZero() {
		t.Error("TakenAt not set")
	}
}

// A partial snapshot silently turns "dies" into "survives" for the
// dependency graph too: an omitted Service or EndpointSlice would make an
// impaired workload look clean of dependencies. Must be an error, same as
// every other List.
func TestFetchFailsClosedOnServiceListError(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "services",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("forbidden")
		})

	_, err := Fetch(context.Background(), cs)
	if err == nil {
		t.Fatal("Fetch() returned nil error on a failed List")
	}
	if !strings.Contains(err.Error(), "services") {
		t.Errorf("error %q does not name the failing resource", err)
	}
}

func TestFetchFailsClosedOnEndpointSliceListError(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "endpointslices",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("forbidden")
		})

	_, err := Fetch(context.Background(), cs)
	if err == nil {
		t.Fatal("Fetch() returned nil error on a failed List")
	}
	if !strings.Contains(err.Error(), "endpointslices") {
		t.Errorf("error %q does not name the failing resource", err)
	}
}

// A partial snapshot silently turns "dies" into "survives". It must be an error.
func TestFetchFailsClosedOnListError(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "poddisruptionbudgets",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("forbidden")
		})

	_, err := Fetch(context.Background(), cs)
	if err == nil {
		t.Fatal("Fetch() returned nil error on a failed List")
	}
	if !strings.Contains(err.Error(), "poddisruptionbudgets") {
		t.Errorf("error %q does not name the failing resource", err)
	}
}

package sched

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestNewBuildsAFrameworkWithTheDefaultFilterPlugins(t *testing.T) {
	s, err := New(context.Background(), snapshotWith(
		[]*corev1.Node{node("n-a", "zone-a"), node("n-b", "zone-b")},
		[]*corev1.Pod{assignedPod("web-1", "n-a", map[string]string{"app": "web"})},
	))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := len(s.framework.ListPlugins().Filter.Enabled); got < 10 {
		t.Fatalf("expected the default profile's filter plugins, got %d", got)
	}
	if len(s.Nodes()) != 2 {
		t.Fatalf("Nodes() = %d, want 2", len(s.Nodes()))
	}
}

func TestNewIsCallableTwiceInOneProcess(t *testing.T) {
	// Scheduler metrics register into a package-level registry. A second
	// registration must not panic or error, or a long-lived exporter dies.
	for i := 0; i < 2; i++ {
		if _, err := New(context.Background(), snapshotWith(
			[]*corev1.Node{node("n-a", "zone-a")}, nil)); err != nil {
			t.Fatalf("New #%d: %v", i+1, err)
		}
	}
}

//go:build harness

package harness

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClusterBootstraps(t *testing.T) {
	c := NewCluster(t, "survive-bootstrap")
	c.AddNode(t, "kwok-1a-0", "us-east-1a")
	c.AddNode(t, "kwok-1b-0", "us-east-1b")

	nodes, err := c.Client.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes.Items) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes.Items))
	}

	c.Apply(t, testDeployment("boot", 2))
	c.WaitPodsReady(t, "default", 2, 60*time.Second)
}

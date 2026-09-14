//go:build harness

// Package harness runs real KWOK clusters for differential testing.
//
// KWOK runs genuine upstream etcd, kube-apiserver, kube-controller-manager and
// kube-scheduler binaries; only node and pod lifecycle is faked. Verified
// 2026-09-13: the disruption controller really computes PDB status, the
// eviction API really returns 429 when a budget blocks, and the real scheduler
// really honours topology spread.
package harness

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// KubeVersion pins the control plane. KWOK selects component versions from the
// KWOK_KUBE_VERSION environment variable; there is no --kube-version flag.
const KubeVersion = "v1.35.7"

type Cluster struct {
	Name   string
	Client kubernetes.Interface
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(cmd.Environ(), "KWOK_KUBE_VERSION="+KubeVersion)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func NewCluster(t *testing.T, name string) *Cluster {
	t.Helper()
	run(t, "kwokctl", "create", "cluster", "--name", name)
	t.Cleanup(func() {
		exec.Command("kwokctl", "delete", "cluster", "--name", name).Run()
	})

	kubeconfig := run(t, "kwokctl", "get", "kubeconfig", "--name", name)
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return &Cluster{Name: name, Client: cs}
}

func (c *Cluster) AddNode(t *testing.T, name, zone string) {
	t.Helper()
	c.Apply(t, fmt.Sprintf(`
apiVersion: v1
kind: Node
metadata:
  name: %s
  labels:
    type: kwok
    topology.kubernetes.io/zone: %s
    kubernetes.io/hostname: %s
  annotations:
    node.alpha.kubernetes.io/ttl: "0"
    kwok.x-k8s.io/node: fake
status:
  allocatable: {cpu: "16", memory: 64Gi, pods: "110"}
  capacity: {cpu: "16", memory: 64Gi, pods: "110"}
  nodeInfo: {kubeletVersion: fake}
  phase: Running
  conditions:
  - {type: Ready, status: "True", reason: KubeletReady}
`, name, zone, name))
}

func (c *Cluster) Apply(t *testing.T, manifest string) {
	t.Helper()
	cmd := exec.Command("kubectl", "--context", "kwok-"+c.Name, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl apply failed: %v\n%s", err, out)
	}
}

func (c *Cluster) WaitPodsReady(t *testing.T, namespace string, count int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pods, err := c.Client.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
		if err == nil {
			ready := 0
			for i := range pods.Items {
				for _, cond := range pods.Items[i].Status.Conditions {
					if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
						ready++
						break // count each pod at most once
					}
				}
			}
			if ready >= count {
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %d ready pods in %s", count, namespace)
}

func (c *Cluster) ApplyNamespace(t *testing.T, name string) {
	t.Helper()
	c.Apply(t, fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata: {name: %s}\n", name))
}

func (c *Cluster) DeleteNamespace(t *testing.T, name string) {
	t.Helper()
	exec.Command("kubectl", "--context", "kwok-"+c.Name, "delete", "namespace", name, "--wait=false").Run()
}

// testDeployment renders a Deployment whose pods tolerate KWOK's fake-node taint.
func testDeployment(name string, replicas int) string {
	return fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata: {name: %s}
spec:
  replicas: %d
  selector: {matchLabels: {app: %s}}
  template:
    metadata: {labels: {app: %s}}
    spec:
      tolerations: [{key: kwok.x-k8s.io/node, operator: Exists, effect: NoSchedule}]
      containers:
      - name: c
        image: nginx
        resources: {requests: {cpu: 100m, memory: 128Mi}}
`, name, replicas, name, name)
}

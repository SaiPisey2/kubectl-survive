// Package sched answers scheduling questions with the real scheduler rather
// than with approximate capacity arithmetic. Approximations are wrong exactly
// at the margin, and a tight cluster lives at the margin.
package sched

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/apis/config/latest"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	schedframework "k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	schedmetrics "k8s.io/kubernetes/pkg/scheduler/metrics"

	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
)

// The framework instruments every plugin at construction time from package-level
// metric vectors that stay nil until registration runs, so NewFramework panics
// without this. Registration is process-wide and must happen exactly once.
var registerMetrics sync.Once

// Long enough that nothing expires during an analysis run; the cache is never
// fed real bind events, so the assumed-pod expiry machinery must stay idle.
const cacheTTL = time.Hour

// Scheduler answers scheduling questions about one point-in-time snapshot.
// It is not safe for concurrent use.
type Scheduler struct {
	framework schedframework.Framework
	cache     cache.Cache
	// snapshot is the very object the framework was constructed with. The cache
	// mutates it in place, which is the only way a simulated placement becomes
	// visible to the plugins.
	snapshot *cache.Snapshot
	logger   klog.Logger
	nodes    []*corev1.Node
}

// New builds a framework with the default scheduler profile over the snapshot's
// nodes and assigned pods.
func New(ctx context.Context, s *snapshot.Snapshot) (*Scheduler, error) {
	registerMetrics.Do(schedmetrics.Register)
	logger := klog.FromContext(ctx)

	// A Cache rather than a bare snapshot: the fix ladder needs to add a
	// simulated pod and have the plugins see it, and only the cache can refresh
	// the snapshot the framework already holds.
	c := cache.New(ctx, cacheTTL, nil)
	for _, n := range s.Nodes {
		c.AddNode(logger, n)
	}
	assigned := assignedPods(s.Pods)
	for _, p := range assigned {
		if err := c.AddPod(logger, p); err != nil {
			return nil, fmt.Errorf("seed pod %s/%s: %w", p.Namespace, p.Name, err)
		}
	}
	snap := cache.NewEmptySnapshot()
	if err := c.UpdateSnapshot(logger, snap); err != nil {
		return nil, fmt.Errorf("build node snapshot: %w", err)
	}

	cfg, err := latest.Default()
	if err != nil {
		return nil, fmt.Errorf("default scheduler profile: %w", err)
	}
	if len(cfg.Profiles) == 0 {
		return nil, fmt.Errorf("default scheduler profile: no profiles")
	}
	profile := cfg.Profiles[0]

	// The plugins read cluster objects through listers, not through the API.
	// A fake clientset backed by the snapshot gives them exactly the objects the
	// analysis already fetched, and nothing else. interpodaffinity nil-panics
	// without an informer factory, so it is not optional.
	client := fake.NewSimpleClientset(objects(s.Nodes, assigned)...)
	factory := informers.NewSharedInformerFactory(client, 0)

	f, err := frameworkruntime.NewFramework(ctx, plugins.NewInTreeRegistry(), &profile,
		frameworkruntime.WithClientSet(client),
		frameworkruntime.WithInformerFactory(factory),
		frameworkruntime.WithSnapshotSharedLister(snap),
	)
	if err != nil {
		return nil, fmt.Errorf("build scheduler framework: %w", err)
	}

	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())

	return &Scheduler{framework: f, cache: c, snapshot: snap, logger: logger, nodes: s.Nodes}, nil
}

// Nodes returns the nodes the framework was built over, in snapshot order.
func (s *Scheduler) Nodes() []*corev1.Node { return s.nodes }

// assignedPods keeps only pods that occupy a node, because only those consume
// capacity or contribute to spread and affinity counts.
func assignedPods(pods []*corev1.Pod) []*corev1.Pod {
	out := make([]*corev1.Pod, 0, len(pods))
	for _, p := range pods {
		if p.Spec.NodeName == "" {
			continue
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		out = append(out, p)
	}
	return out
}

func objects(nodes []*corev1.Node, pods []*corev1.Pod) []runtime.Object {
	out := make([]runtime.Object, 0, len(nodes)+len(pods))
	for _, n := range nodes {
		out = append(out, n)
	}
	for _, p := range pods {
		out = append(out, p)
	}
	return out
}

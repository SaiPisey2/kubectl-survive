package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/fix"
	"github.com/SaiPisey2/kubectl-survive/internal/sched"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/verify"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
)

func TestFixCommandExists(t *testing.T) {
	streams := genericiooptions.IOStreams{In: &bytes.Buffer{}, Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}
	root := NewCmd(streams)

	fixCmd, _, err := root.Find([]string{"fix"})
	if err != nil {
		t.Fatalf("fix subcommand not registered: %v", err)
	}
	for _, f := range []string{"domain-key", "output", "out-dir", "kubeconfig", "context", "namespace"} {
		if fixCmd.Flags().Lookup(f) == nil {
			t.Errorf("fix command missing flag %q", f)
		}
	}
	for _, f := range []string{"apply", "pr"} {
		if fixCmd.Flags().Lookup(f) != nil {
			t.Errorf("fix command must not implement --%s in this milestone", f)
		}
	}
	if strings.Contains(fixCmd.Long, "--apply") || strings.Contains(fixCmd.Long, "--pr") {
		t.Errorf("help text must not mention --apply or --pr: %q", fixCmd.Long)
	}
}

// node builds a ready, schedulable node in the given zone with plenty of
// capacity, mirroring internal/verify's own test fixtures.
func node(name, zone string) *corev1.Node {
	capacity := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("8"),
		corev1.ResourceMemory: resource.MustParse("32Gi"),
		corev1.ResourcePods:   resource.MustParse("110"),
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  types.UID(name),
			Labels: map[string]string{
				domain.LabelZone:     zone,
				corev1.LabelHostname: name,
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: capacity,
			Capacity:    capacity,
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

// ownedPod builds a running pod on node, owned by a Deployment named
// "workload" in namespace "default".
func ownedPod(name, node string, labels map[string]string) *corev1.Pod {
	controller := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID(name),
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				Kind:       "Deployment",
				Name:       "workload",
				UID:        types.UID("workload"),
				Controller: &controller,
			}},
		},
		Spec: corev1.PodSpec{
			NodeName:   node,
			Containers: []corev1.Container{{Name: "app", Image: "example.test/app:latest"}},
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

// lostSnapshot is a single zone, single node, single (unspread) pod: the
// workload it holds loses availability the instant that zone is lost.
func lostSnapshot() *snapshot.Snapshot {
	return &snapshot.Snapshot{
		TakenAt: time.Now(),
		Nodes:   []*corev1.Node{node("n-a", "zone-a")},
		Pods:    []*corev1.Pod{ownedPod("workload-0", "n-a", map[string]string{"app": "workload"})},
	}
}

func TestFixPrintsTheVersionGateWarningAndStillReportsSurvivability(t *testing.T) {
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	o := &FixOptions{
		Streams:   genericiooptions.IOStreams{In: &bytes.Buffer{}, Out: out, ErrOut: errOut},
		DomainKey: domain.LabelZone,
		Output:    "table",
	}

	gate := sched.Gate("v1.31.6")
	if gate.Enabled {
		t.Fatalf("expected a mismatched minor to disable the gate")
	}

	if err := o.runWithSnapshot(context.Background(), lostSnapshot(), gate); err != nil {
		t.Fatalf("runWithSnapshot: %v", err)
	}

	if !strings.Contains(errOut.String(), "Fix verification:") {
		t.Fatalf("want the named version-gate warning on stderr, got: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "DISABLED") {
		t.Fatalf("want the warning to say fix verification is disabled, got: %q", errOut.String())
	}
	if !strings.Contains(out.String(), "workload") {
		t.Fatalf("want the survivability report to still print on a version mismatch, got: %q", out.String())
	}
}

func TestFixTreatsADiscoveryErrorLikeAMismatch(t *testing.T) {
	// An empty server version string is what Run passes through when
	// discovery itself errors; it must never be treated as a match.
	gate := sched.Gate("")
	if gate.Enabled {
		t.Fatal("an unreadable server version must never be treated as a match")
	}
}

func verifiedFix(rung fix.Rung, patch string) verify.Verified {
	return verify.Verified{
		Fix: fix.Fix{Rung: rung, Title: "a fix", Patch: patch},
		Proof: verify.Proof{
			Schedulable: true, SchedulableDetail: "fits",
			Survives: true, SurvivesDetail: "stays up",
		},
	}
}

func TestFixWritesOnePatchFilePerFix(t *testing.T) {
	dir := t.TempDir()
	results := []verify.Result{
		{
			Workload: workload.Ref{Kind: "Deployment", Namespace: "default", Name: "checkout-api"},
			Domain:   "us-east-1a",
			Fixes: []verify.Verified{
				verifiedFix(fix.RungSpreadAdd, "patch-one"),
				verifiedFix(fix.RungReplicasRaise, "patch-two"),
			},
		},
		{
			Workload: workload.Ref{Kind: "Deployment", Namespace: "billing", Name: "payments-worker"},
			Domain:   "us-east-1a",
			Fixes:    []verify.Verified{verifiedFix(fix.RungPDBAdd, "patch-three")},
		},
	}

	if err := writeFixPatches(dir, results); err != nil {
		t.Fatalf("writeFixPatches: %v", err)
	}

	want := map[string]string{
		"default-checkout-api-rung1.yaml":    "patch-one",
		"default-checkout-api-rung4.yaml":    "patch-two",
		"billing-payments-worker-rung5.yaml": "patch-three",
	}
	for name, contents := range want {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("expected patch file %s: %v", name, err)
		}
		if string(got) != contents {
			t.Errorf("file %s = %q, want %q", name, got, contents)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != len(want) {
		t.Fatalf("want exactly %d patch files, got %d: %v", len(want), len(entries), entries)
	}
}

func TestFixRefusesToWriteOutsideTheOutputDirectory(t *testing.T) {
	dir := t.TempDir()
	results := []verify.Result{{
		Workload: workload.Ref{Kind: "Deployment", Namespace: "default", Name: "../../etc/evil"},
		Domain:   "us-east-1a",
		Fixes:    []verify.Verified{verifiedFix(fix.RungSpreadAdd, "malicious patch")},
	}}

	if err := writeFixPatches(dir, results); err == nil {
		t.Fatal("want an error when a workload name contains a path separator")
	}

	// Nothing must have been written anywhere, inside or outside dir.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want no files written when the name is unsafe, got: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "etc")); err == nil {
		t.Fatal("a file escaped the output directory")
	}
}

func TestFixRefusesAWorkloadNameThatIsAPathSeparator(t *testing.T) {
	dir := t.TempDir()
	results := []verify.Result{{
		Workload: workload.Ref{Kind: "Deployment", Namespace: "default", Name: "a/b"},
		Domain:   "us-east-1a",
		Fixes:    []verify.Verified{verifiedFix(fix.RungSpreadAdd, "patch")},
	}}

	if err := writeFixPatches(dir, results); err == nil {
		t.Fatal("want an error for a workload name embedding a path separator")
	}
}

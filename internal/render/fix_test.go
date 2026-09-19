package render

import (
	"os"
	"strings"
	"testing"

	"github.com/SaiPisey2/kubectl-survive/internal/fix"
	"github.com/SaiPisey2/kubectl-survive/internal/verify"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
)

func golden(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file %s: %v", path, err)
	}
	return string(b)
}

func TestFixesMatchesTheSpecExampleForASpreadFix(t *testing.T) {
	results := []verify.Result{{
		Workload: workload.Ref{Kind: "Deployment", Namespace: "default", Name: "checkout-api"},
		Domain:   "us-east-1a",
		Cause:    "3/3 replicas in 1a, no spread constraint",
		Fixes: []verify.Verified{{
			Fix: fix.Fix{
				Rung:  fix.RungSpreadAdd,
				Title: "add topologySpreadConstraint  maxSkew=1  zone  DoNotSchedule",
				Patch: "spec:\n" +
					"  template:\n" +
					"    spec:\n" +
					"+     topologySpreadConstraints:\n" +
					"+     - maxSkew: 1\n" +
					"+       topologyKey: topology.kubernetes.io/zone\n" +
					"+       whenUnsatisfiable: DoNotSchedule\n" +
					"+       labelSelector:\n" +
					"+         matchLabels: {app: checkout-api}",
			},
			Proof: verify.Proof{
				Schedulable:       true,
				SchedulableDetail: "1a:1  1b:1  1c:1   (12 nodes have room)",
				Survives:          true,
				SurvivesDetail:    "2 of 3 replicas remain",
			},
			Partial: false,
		}},
	}}

	var sb strings.Builder
	if err := Fixes(&sb, results); err != nil {
		t.Fatalf("Fixes: %v", err)
	}

	want := golden(t, "testdata/fix_spread.golden")
	if sb.String() != want {
		t.Fatalf("output does not match golden file.\n--- got ---\n%s\n--- want ---\n%s", sb.String(), want)
	}
}

func TestFixesMatchesTheSpecExampleForAnArchitecturalFinding(t *testing.T) {
	results := []verify.Result{{
		Workload: workload.Ref{Kind: "StatefulSet", Namespace: "default", Name: "ledger-db"},
		Domain:   "us-east-1a",
		Architectural: []fix.Fix{{
			Rung:  fix.RungVolumePin,
			Title: "Zonal volume pins this workload to its domain",
			Architectural: "PersistentVolume pvc-8f21ac is zonal (nodeAffinity -> us-east-1a). " +
				"The pod cannot run anywhere else. No manifest change moves it.",
		}},
	}}

	var sb strings.Builder
	if err := Fixes(&sb, results); err != nil {
		t.Fatalf("Fixes: %v", err)
	}

	want := golden(t, "testdata/fix_architectural.golden")
	if sb.String() != want {
		t.Fatalf("output does not match golden file.\n--- got ---\n%s\n--- want ---\n%s", sb.String(), want)
	}
}

func TestFixesLabelsAPartialFixAsALT(t *testing.T) {
	// Spec 6.2: a fix that schedules but does not make the workload survive
	// must render as visibly partial -- ALT, with NO on the survives line.
	results := []verify.Result{{
		Workload: workload.Ref{Kind: "Deployment", Namespace: "default", Name: "session-store"},
		Domain:   "us-east-1a",
		Cause:    "PDB minAvailable=1, replicas=1",
		Fixes: []verify.Verified{
			{
				Fix: fix.Fix{Rung: fix.RungReplicasRaise, Title: "replicas 1 -> 3, keep minAvailable=1"},
				Proof: verify.Proof{
					Schedulable: true, SchedulableDetail: "2 more pods fit",
					Survives: true, SurvivesDetail: "and drains no longer block",
				},
				Partial: false,
			},
			{
				Fix: fix.Fix{Rung: fix.RungPDBRepair, Title: "minAvailable=1 -> maxUnavailable=1"},
				Proof: verify.Proof{
					Schedulable: true, SchedulableDetail: "no placement change",
					Survives: false, SurvivesDetail: "unblocks drains only, still single-zone",
				},
				Partial: true,
			},
		},
	}}

	var sb strings.Builder
	if err := Fixes(&sb, results); err != nil {
		t.Fatalf("Fixes: %v", err)
	}
	out := sb.String()

	if !strings.Contains(out, "  FIX  replicas 1 -> 3") {
		t.Fatalf("want the full fix labelled FIX, got:\n%s", out)
	}
	if !strings.Contains(out, "  ALT  minAvailable=1 -> maxUnavailable=1") {
		t.Fatalf("want the partial fix labelled ALT, got:\n%s", out)
	}
	altIdx := strings.Index(out, "ALT")
	if altIdx == -1 {
		t.Fatalf("no ALT block found:\n%s", out)
	}
	altBlock := out[altIdx:]
	if !strings.Contains(altBlock, "survives us-east-1a?  NO") {
		t.Fatalf("the ALT block must carry NO on the survives line, got:\n%s", altBlock)
	}
	if strings.Contains(altBlock, "survives us-east-1a?  yes") {
		t.Fatalf("a partial fix must never read as though it solved the problem:\n%s", altBlock)
	}
}

func TestFixesPrintsNothingForAWorkloadWithNoFindings(t *testing.T) {
	results := []verify.Result{{
		Workload: workload.Ref{Kind: "Deployment", Namespace: "default", Name: "steady"},
		Domain:   "us-east-1a",
		Cause:    "should never be printed",
	}}

	var sb strings.Builder
	if err := Fixes(&sb, results); err != nil {
		t.Fatalf("Fixes: %v", err)
	}
	if sb.String() != "" {
		t.Fatalf("want no output for a workload with no fixes and no architectural findings, got:\n%s", sb.String())
	}
}

func TestFixesJSONEmitsOneEntryPerWorkloadWithFindings(t *testing.T) {
	results := []verify.Result{
		{
			Workload: workload.Ref{Kind: "Deployment", Namespace: "default", Name: "checkout-api"},
			Domain:   "us-east-1a",
			Cause:    "no spread constraint",
			Fixes: []verify.Verified{{
				Fix:     fix.Fix{Rung: fix.RungSpreadAdd, Title: "add spread"},
				Proof:   verify.Proof{Schedulable: true, Survives: true},
				Partial: false,
			}},
		},
		{
			Workload: workload.Ref{Kind: "Deployment", Namespace: "default", Name: "steady"},
			Domain:   "us-east-1a",
		},
	}

	var sb strings.Builder
	if err := FixesJSON(&sb, results); err != nil {
		t.Fatalf("FixesJSON: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "checkout-api") {
		t.Fatalf("want checkout-api in the JSON output, got:\n%s", out)
	}
	if strings.Contains(out, "steady") {
		t.Fatalf("a workload with no findings must not appear, got:\n%s", out)
	}
}

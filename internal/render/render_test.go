package render

import (
	"bytes"
	"encoding/json"
	"github.com/SaiPisey2/kubectl-survive/internal/pdbcheck"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SaiPisey2/kubectl-survive/internal/depgraph"
	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/draincheck"
	"github.com/SaiPisey2/kubectl-survive/internal/spread"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	"github.com/SaiPisey2/kubectl-survive/internal/volumepin"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
)

var update = os.Getenv("UPDATE_GOLDEN") == "1"

func sampleReport() *survive.Report {
	return &survive.Report{
		TakenAt:   time.Date(2026, 9, 12, 9, 14, 3, 0, time.UTC),
		DomainKey: domain.LabelZone,
		Domains: []survive.DomainResult{{
			Domain: "us-east-1a",
			Lost:   1,
			Verdicts: []survive.Verdict{{
				Workload:  workload.Ref{Kind: "Deployment", Namespace: "default", Name: "checkout-api"},
				Outcome:   survive.OutcomeLost,
				Reason:    "all 3 available replicas are in us-east-1a",
				Placement: map[string]int{"us-east-1a": 3},
			}},
		}, {
			// A domain where nothing is wrong. The renderer must print its
			// summary line and nothing else: a table header with no rows under
			// it reads as a truncated table rather than as good news. This case
			// had no golden coverage, which is why it regressed unnoticed.
			Domain: "us-east-1b",
			Verdicts: []survive.Verdict{{
				Workload:  workload.Ref{Kind: "Deployment", Namespace: "default", Name: "checkout-api"},
				Outcome:   survive.OutcomeSurvives,
				Placement: map[string]int{"us-east-1a": 3},
			}},
		}},
	}
}

func TestTableGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, sampleReport()); err != nil {
		t.Fatalf("Table() error = %v", err)
	}
	golden := filepath.Join("testdata", "basic.golden")
	if update {
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v (run with UPDATE_GOLDEN=1 to create)", err)
	}
	if buf.String() != string(want) {
		t.Errorf("output mismatch\n--- got ---\n%s\n--- want ---\n%s", buf.String(), want)
	}
}

// reportWithImpairment covers the dependency layer added for spec §5.5: a
// workload whose own pods survive the domain's loss but that is impaired
// because a Service-resolved dependency is lost there. Its own Outcome stays
// Survives -- ruling 1 -- but it must still appear in the table because the
// domain has something worth reporting, even with zero Lost/Degraded.
func reportWithImpairment() *survive.Report {
	return &survive.Report{
		TakenAt:   time.Date(2026, 9, 12, 9, 14, 3, 0, time.UTC),
		DomainKey: domain.LabelZone,
		Domains: []survive.DomainResult{{
			Domain:   "us-east-1a",
			Impaired: 1,
			Verdicts: []survive.Verdict{{
				Workload:  workload.Ref{Kind: "Deployment", Namespace: "default", Name: "web"},
				Outcome:   survive.OutcomeSurvives,
				Reason:    "2 of 2 replicas remain outside us-east-1a",
				Placement: map[string]int{"us-east-1a": 1, "us-east-1b": 1},
				DependsOn: []string{"default/session-store"},
			}, {
				Workload:  workload.Ref{Kind: "StatefulSet", Namespace: "default", Name: "session-store"},
				Outcome:   survive.OutcomeLost,
				Reason:    "all 1 available replicas are in us-east-1a",
				Placement: map[string]int{"us-east-1a": 1},
			}},
			Impairments: []depgraph.Impairment{{
				Workload: workload.Ref{Kind: "Deployment", Namespace: "default", Name: "web"},
				Domain:   "us-east-1a",
				Chain:    []string{"web", "session-store"},
			}},
		}},
	}
}

func TestTableGoldenImpaired(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, reportWithImpairment()); err != nil {
		t.Fatalf("Table() error = %v", err)
	}
	golden := filepath.Join("testdata", "impaired.golden")
	if update {
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v (run with UPDATE_GOLDEN=1 to create)", err)
	}
	if buf.String() != string(want) {
		t.Errorf("output mismatch\n--- got ---\n%s\n--- want ---\n%s", buf.String(), want)
	}
}

func TestJSONIncludesDependsOnAndImpairments(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, reportWithImpairment()); err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	var out struct {
		Domains []struct {
			Impaired  int `json:"workloadsImpaired"`
			Workloads []struct {
				Name      string   `json:"name"`
				DependsOn []string `json:"dependsOn"`
			} `json:"workloads"`
			Impairments []struct {
				Name  string   `json:"name"`
				Chain []string `json:"chain"`
			} `json:"impairments"`
		} `json:"domains"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	d := out.Domains[0]
	if d.Impaired != 1 {
		t.Errorf("workloadsImpaired = %d, want 1", d.Impaired)
	}
	if len(d.Impairments) != 1 || d.Impairments[0].Name != "web" {
		t.Fatalf("impairments = %+v, want one entry for web", d.Impairments)
	}
	wantChain := []string{"web", "session-store"}
	if len(d.Impairments[0].Chain) != 2 || d.Impairments[0].Chain[0] != wantChain[0] || d.Impairments[0].Chain[1] != wantChain[1] {
		t.Errorf("chain = %v, want %v", d.Impairments[0].Chain, wantChain)
	}
}

// AntiAffinity and VolumePins are computed by the engine but were previously
// dropped on the floor by both renderers: §5.4 and §8.3 of the design doc
// require them to reach the user.
func reportWithPin() *survive.Report {
	return &survive.Report{
		TakenAt:   time.Date(2026, 9, 12, 9, 14, 3, 0, time.UTC),
		DomainKey: domain.LabelZone,
		Domains: []survive.DomainResult{{
			Domain: "us-east-1a",
			Lost:   1,
			Verdicts: []survive.Verdict{{
				Workload:     workload.Ref{Kind: "StatefulSet", Namespace: "default", Name: "db"},
				Outcome:      survive.OutcomeLost,
				Reason:       "every surviving replica is pinned to us-east-1a by a zonal volume",
				Placement:    map[string]int{"us-east-1a": 1},
				AntiAffinity: spread.Assessment{State: spread.StateAbsent, Detail: "no pod anti-affinity"},
				VolumePins:   []volumepin.Pin{{PVC: "data-db-0", PV: "pv-db-1", Domain: "us-east-1a"}},
			}},
		}},
	}
}

func TestTableRendersVolumePins(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, reportWithPin()); err != nil {
		t.Fatalf("Table() error = %v", err)
	}
	if !strings.Contains(buf.String(), "volume pv-db-1 is pinned to us-east-1a") {
		t.Errorf("table output does not mention the volume pin:\n%s", buf.String())
	}
}

func TestJSONIncludesAntiAffinityAndVolumePins(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, reportWithPin()); err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	var out struct {
		Domains []struct {
			Workloads []struct {
				AntiAffinity string   `json:"antiAffinity"`
				VolumePins   []string `json:"volumePins"`
			} `json:"workloads"`
		} `json:"domains"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	w := out.Domains[0].Workloads[0]
	if w.AntiAffinity != string(spread.StateAbsent) {
		t.Errorf("antiAffinity = %q, want %q", w.AntiAffinity, spread.StateAbsent)
	}
	if len(w.VolumePins) != 1 || w.VolumePins[0] != "pv-db-1@us-east-1a" {
		t.Errorf("volumePins = %v, want [pv-db-1@us-east-1a]", w.VolumePins)
	}
}

// TestTableRendersDrainDeadlockFindings pins the drain-deadlock line style
// against the existing PDB finding line, per spec 10.4: an operator reading
// this output should not be able to tell, from formatting alone, that one
// came from static PDB arithmetic and the other from the scheduler-backed
// check in internal/draincheck.
func TestTableRendersDrainDeadlockFindings(t *testing.T) {
	var buf bytes.Buffer
	deadlock := draincheck.Finding{
		PDB:       "web-pdb",
		Namespace: "default",
		Domain:    "us-east-1a",
		Detail:    "draining us-east-1a deadlocks: default/web's replacement cannot schedule outside it (node(s) didn't match Pod's node affinity/selector); the budget then refuses every later eviction",
	}
	if err := Table(&buf, sampleReport(), deadlock); err != nil {
		t.Fatalf("Table() error = %v", err)
	}
	want := "PDB default/web-pdb: draining us-east-1a deadlocks: default/web's replacement cannot schedule outside it (node(s) didn't match Pod's node affinity/selector); the budget then refuses every later eviction\n"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("table output does not contain the expected drain-deadlock line\ngot:\n%s\nwant substring:\n%s", buf.String(), want)
	}
}

func TestJSONShape(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, sampleReport()); err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if out["apiVersion"] != "survive.dev/v1alpha1" {
		t.Errorf("apiVersion = %v, want survive.dev/v1alpha1", out["apiVersion"])
	}
	if _, ok := out["snapshotAt"]; !ok {
		t.Error("missing snapshotAt")
	}
}

// The JSON output carried no drain findings at all until 2026-09-22: a machine
// consumer could not learn that a budget would hang a drain, while the table
// said so plainly. These pin both detectors into the schema.
func TestJSONCarriesBothKindsOfDrainFinding(t *testing.T) {
	r := sampleReport()
	r.PDBFindings = []pdbcheck.Finding{{
		PDB: "session-store", Namespace: "default",
		Block: pdbcheck.BlockNeverSatisfiable, Detail: "minAvailable resolves to 1 of 1 pods",
	}}
	deadlock := draincheck.Finding{
		PDB: "checkout-api", Namespace: "default", Domain: "us-east-1a",
		Detail: "draining us-east-1a deadlocks",
	}

	var buf bytes.Buffer
	if err := JSON(&buf, r, deadlock); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var got struct {
		Findings []struct {
			PDB, Namespace, Kind, Domain, Detail string
		} `json:"drainFindings"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if len(got.Findings) != 2 {
		t.Fatalf("want both findings, got %d: %s", len(got.Findings), buf.String())
	}
	kinds := map[string]string{}
	for _, f := range got.Findings {
		kinds[f.Kind] = f.PDB
	}
	if kinds["unsatisfiable"] != "session-store" {
		t.Errorf("unsatisfiable budget missing from JSON: %v", kinds)
	}
	if kinds["drainDeadlock"] != "checkout-api" {
		t.Errorf("drain deadlock missing from JSON: %v", kinds)
	}
}

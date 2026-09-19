package verify

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	policyv1 "k8s.io/api/policy/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/SaiPisey2/kubectl-survive/internal/fix"
	"github.com/SaiPisey2/kubectl-survive/internal/spread"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	"github.com/SaiPisey2/kubectl-survive/internal/volumepin"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
)

// deployment builds a real Deployment object so resolveWorkload can find a
// pod template, replica count and selector for the given ref.
func deployment(name string, replicas int32, tmpl *corev1.PodTemplateSpec) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: tmpl.Labels},
			Template: *tmpl,
		},
	}
}

// pdbSatisfiable builds a budget that already tolerates one unavailable
// replica out of three -- used to suppress rungs 5 and 6 in fixtures that are
// not about the disruption budget at all.
func pdbSatisfiable(name string, selector map[string]string) *policyv1.PodDisruptionBudget {
	mu := intstr.FromInt32(1)
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &mu,
			Selector:       &metav1.LabelSelector{MatchLabels: selector},
		},
	}
}

// lostVerdict is the common case: a workload the survivability engine already
// found lost in domain, with the given spread state and no volume pins unless
// set separately.
func lostVerdict(ref workload.Ref, tmpl *corev1.PodTemplateSpec) survive.Verdict {
	return survive.Verdict{
		Workload: ref,
		Outcome:  survive.OutcomeLost,
		Reason:   "all replicas in one domain",
		Spread:   spread.Assessment{State: spread.StateAbsent},
	}
}

func TestAnalyzeOnlyReportsWorkloadsThatActuallyFail(t *testing.T) {
	// A workload the engine says survives has nothing to fix. Emitting advice
	// for it turns the tool into a linter, which is what it is not.
	web := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "web"}
	steady := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "steady"}

	webTmpl := webTemplate()
	steadyTmpl := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "steady"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example.test/app:latest"}}},
	}

	pods := threeReplicasAllIn("zone-a")
	snap := snapshotWith(threeZonesOneNodeEach(), pods)
	snap.Deployments = []*appsv1.Deployment{deployment("web", 3, webTmpl), deployment("steady", 3, steadyTmpl)}

	report := &survive.Report{
		DomainKey: zoneKey,
		Domains: []survive.DomainResult{{
			Domain: "zone-a",
			Lost:   1,
			Verdicts: []survive.Verdict{
				lostVerdict(web, webTmpl),
				{Workload: steady, Outcome: survive.OutcomeSurvives, Reason: "spread across zones"},
			},
		}},
	}

	results, err := Analyze(context.Background(), snap, report, zoneKey, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result (only the failing workload), got %d: %+v", len(results), results)
	}
	if results[0].Workload != web {
		t.Fatalf("want the failing workload web, got %+v", results[0].Workload)
	}
}

func TestAnalyzeDropsCandidatesThatFailBothProofs(t *testing.T) {
	// Not schedulable and not surviving: printing it would be noise at best and
	// an outage at worst.
	ref := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "web"}
	tmpl := webTemplate()

	pods := threeReplicasAllIn("zone-a")
	snap := snapshotWith(oneUsableZoneTwoTainted(), pods)
	snap.Deployments = []*appsv1.Deployment{deployment("web", 3, tmpl)}

	report := &survive.Report{
		DomainKey: zoneKey,
		Domains: []survive.DomainResult{{
			Domain:   "zone-a",
			Lost:     1,
			Verdicts: []survive.Verdict{lostVerdict(ref, tmpl)},
		}},
	}

	results, err := Analyze(context.Background(), snap, report, zoneKey, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	for _, v := range results[0].Fixes {
		if v.Fix.Rung == fix.RungSpreadAdd {
			t.Fatalf("a spread fix that cannot schedule on this cluster must be dropped, got %+v", v)
		}
	}
}

func TestAnalyzeMarksASchedulableNonSurvivingFixAsPartial(t *testing.T) {
	// Spec 6.2: this is the difference between a trustworthy tool and a
	// checkbox generator.
	// oneReplicaIn's pod is owned by "Deployment/default/workload" (see
	// helpers_test.go's ownedPod), so the workload's identity here must match
	// that, not the app label, for the PDB lookup in pdbsByWorkload to find it.
	ref := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "workload"}
	tmpl := singletonTemplate()

	snap := snapshotWith(threeZonesOneNodeEach(), oneReplicaIn("zone-a"))
	snap.Deployments = []*appsv1.Deployment{deployment("workload", 1, tmpl)}
	snap.PDBs = []*policyv1.PodDisruptionBudget{pdbMinAvailable(1)}
	snap.PDBs[0].Name = "singleton-pdb"
	snap.PDBs[0].Namespace = "default"
	snap.PDBs[0].Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "singleton"}}

	report := &survive.Report{
		DomainKey: zoneKey,
		Domains: []survive.DomainResult{{
			Domain:   "zone-a",
			Lost:     1,
			Verdicts: []survive.Verdict{lostVerdict(ref, tmpl)},
		}},
	}

	results, err := Analyze(context.Background(), snap, report, zoneKey, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	var found *Verified
	for i := range results[0].Fixes {
		if results[0].Fixes[i].Fix.Rung == fix.RungPDBRepair {
			found = &results[0].Fixes[i]
		}
	}
	if found == nil {
		t.Fatalf("want a PDB repair candidate, got %+v", results[0].Fixes)
	}
	if !found.Partial {
		t.Fatal("repairing the budget moves no pods; it must be reported partial, not full")
	}
}

func TestAnalyzeRanksFullFixesAbovePartialOnes(t *testing.T) {
	// The operator reads the first line. It must be the one that solves the
	// problem, with the partial alternative below it as ALT.
	// threeReplicasAllIn's pods are owned by "Deployment/default/workload"
	// (see helpers_test.go's ownedPod), so the identity here must match that
	// for pdbsByWorkload's ownership lookup to find this PDB.
	ref := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "workload"}
	tmpl := webTemplate()

	snap := snapshotWith(threeZonesOneNodeEach(), threeReplicasAllIn("zone-a"))
	snap.Deployments = []*appsv1.Deployment{deployment("workload", 3, tmpl)}
	pdb := pdbMinAvailable(3) // unsatisfiable: 3 pods, all must stay up
	pdb.Name = "web-pdb"
	pdb.Namespace = "default"
	pdb.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}
	snap.PDBs = []*policyv1.PodDisruptionBudget{pdb}

	report := &survive.Report{
		DomainKey: zoneKey,
		Domains: []survive.DomainResult{{
			Domain:   "zone-a",
			Lost:     1,
			Verdicts: []survive.Verdict{lostVerdict(ref, tmpl)},
		}},
	}

	results, err := Analyze(context.Background(), snap, report, zoneKey, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(results) != 1 || len(results[0].Fixes) < 2 {
		t.Fatalf("want at least 2 fixes, got %+v", results)
	}
	fixes := results[0].Fixes
	if fixes[0].Partial {
		t.Fatalf("first fix must be full, got partial rung %d", fixes[0].Fix.Rung)
	}
	if fixes[0].Fix.Rung != fix.RungSpreadAdd {
		t.Fatalf("want the spread fix ranked first, got rung %d", fixes[0].Fix.Rung)
	}
	foundPartial := false
	for _, v := range fixes[1:] {
		if v.Partial && v.Fix.Rung == fix.RungPDBRepair {
			foundPartial = true
		}
		if !v.Partial {
			t.Fatalf("a full fix appears after a partial one: %+v", fixes)
		}
	}
	if !foundPartial {
		t.Fatalf("want the partial PDB repair to still be reported, got %+v", fixes)
	}
}

func TestAnalyzeRespectsTheWorkloadFilter(t *testing.T) {
	web := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "web"}
	other := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "other"}
	webTmpl := webTemplate()
	otherTmpl := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "other"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example.test/app:latest"}}},
	}

	snap := snapshotWith(threeZonesOneNodeEach(), threeReplicasAllIn("zone-a"))
	snap.Deployments = []*appsv1.Deployment{deployment("web", 3, webTmpl), deployment("other", 3, otherTmpl)}

	report := &survive.Report{
		DomainKey: zoneKey,
		Domains: []survive.DomainResult{{
			Domain: "zone-a",
			Lost:   2,
			Verdicts: []survive.Verdict{
				lostVerdict(web, webTmpl),
				lostVerdict(other, otherTmpl),
			},
		}},
	}

	results, err := Analyze(context.Background(), snap, report, zoneKey, []string{"web"})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result honoring the filter, got %d: %+v", len(results), results)
	}
	if results[0].Workload != web {
		t.Fatalf("want web, got %+v", results[0].Workload)
	}
}

func TestAnalyzeSurfacesArchitecturalFindingsEvenWithNoFixes(t *testing.T) {
	// A zone-pinned database has no patch, and that IS the answer. Silence
	// would read as "nothing to do here".
	ref := workload.Ref{Kind: "StatefulSet", Namespace: "default", Name: "db"}
	tmpl := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "db"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example.test/db:latest"}}},
	}

	// Three replicas, one per domain already: rung 4 (replicas < domains)
	// does not fire. A pre-existing satisfiable budget suppresses rungs 5 and
	// 6 so the only candidate left is rung 8's architectural finding.
	pods := []*corev1.Pod{
		ownedStatefulPod("db-0", "n-a", map[string]string{"app": "db"}),
		ownedStatefulPod("db-1", "n-b", map[string]string{"app": "db"}),
		ownedStatefulPod("db-2", "n-c", map[string]string{"app": "db"}),
	}
	snap := snapshotWith(threeZonesOneNodeEach(), pods)
	snap.StatefulSets = []*appsv1.StatefulSet{{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32Ptr(3),
			Selector: &metav1.LabelSelector{MatchLabels: tmpl.Labels},
			Template: *tmpl,
		},
	}}
	snap.PDBs = []*policyv1.PodDisruptionBudget{pdbSatisfiable("db-pdb", map[string]string{"app": "db"})}

	verdict := survive.Verdict{
		Workload:   ref,
		Outcome:    survive.OutcomeLost,
		Reason:     "zonal volume pins every replica to its own domain",
		Spread:     spread.Assessment{State: spread.StateEnforced},
		VolumePins: []volumepin.Pin{{PVC: "data-db-0", PV: "pv-0", Domain: "zone-a"}},
	}
	report := &survive.Report{
		DomainKey: zoneKey,
		Domains: []survive.DomainResult{{
			Domain:   "zone-a",
			Lost:     1,
			Verdicts: []survive.Verdict{verdict},
		}},
	}

	results, err := Analyze(context.Background(), snap, report, zoneKey, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if len(results[0].Fixes) != 0 {
		t.Fatalf("a zonal volume pin has no patch; want zero fixes, got %+v", results[0].Fixes)
	}
	if len(results[0].Architectural) != 1 || results[0].Architectural[0].Rung != fix.RungVolumePin {
		t.Fatalf("want the rung 8 architectural finding surfaced, got %+v", results[0].Architectural)
	}
}

// ownedStatefulPod is ownedPod's StatefulSet-owned counterpart: pods this
// package's fixtures use for a workload resolveWorkload should treat as a
// StatefulSet rather than a Deployment.
func ownedStatefulPod(name, node string, labels map[string]string) *corev1.Pod {
	p := ownedPod(name, node, labels)
	p.OwnerReferences[0].Kind = "StatefulSet"
	p.OwnerReferences[0].Name = "db"
	return p
}

func int32Ptr(n int32) *int32 { return &n }

// TestAnalyzeOffersReplicasRaiseOnAHealthyMultiZoneCluster is a wiring test:
// domain.Group returns (groups, unlabelled), and Analyze must feed the real
// domains -- not the unlabelled-node list, which is empty on any healthy
// cluster -- into fix.Input.Domains. Without that, len(in.Domains) is always
// 0 and rung 4 (raise replicas to the domain count) can never fire, which no
// hand-built fix.Input fixture would ever catch since every other test in
// this package sets Domains directly.
//
// session-store: 1 replica, 3 zones, an unsatisfiable minAvailable:1 budget.
// Spec 6.2's own worked example for this exact shape lists "replicas 1 -> 3"
// as the primary fix, so rung 4 must be offered here.
func TestAnalyzeOffersReplicasRaiseOnAHealthyMultiZoneCluster(t *testing.T) {
	ref := workload.Ref{Kind: "Deployment", Namespace: "default", Name: "workload"}
	tmpl := singletonTemplate()

	snap := snapshotWith(threeZonesOneNodeEach(), oneReplicaIn("zone-a"))
	snap.Deployments = []*appsv1.Deployment{deployment("workload", 1, tmpl)}
	pdb := pdbMinAvailable(1)
	pdb.Name = "singleton-pdb"
	pdb.Namespace = "default"
	pdb.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "singleton"}}
	snap.PDBs = []*policyv1.PodDisruptionBudget{pdb}

	report := &survive.Report{
		DomainKey: zoneKey,
		Domains: []survive.DomainResult{{
			Domain:   "zone-a",
			Lost:     1,
			Verdicts: []survive.Verdict{lostVerdict(ref, tmpl)},
		}},
	}

	results, err := Analyze(context.Background(), snap, report, zoneKey, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	for _, v := range results[0].Fixes {
		if v.Fix.Rung == fix.RungReplicasRaise {
			t.Logf("rung 4 proof: schedulable=%v survives=%v partial=%v detail=%q/%q",
				v.Proof.Schedulable, v.Proof.Survives, v.Partial, v.Proof.SchedulableDetail, v.Proof.SurvivesDetail)
			return
		}
	}
	t.Fatalf("want rung 4 (raise replicas to the domain count) offered on a 3-zone cluster, got %+v", results[0].Fixes)
}

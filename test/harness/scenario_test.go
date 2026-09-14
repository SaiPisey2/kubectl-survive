//go:build harness

package harness

import "testing"

func TestGenerateScenarioIsDeterministic(t *testing.T) {
	a := GenerateScenario(42)
	b := GenerateScenario(42)
	if len(a.Workloads) != len(b.Workloads) {
		t.Fatalf("same seed produced %d and %d workloads", len(a.Workloads), len(b.Workloads))
	}
	for i := range a.Workloads {
		if a.Workloads[i] != b.Workloads[i] {
			t.Errorf("workload %d differs: %+v vs %+v", i, a.Workloads[i], b.Workloads[i])
		}
	}
}

func TestGenerateScenarioVaries(t *testing.T) {
	seen := map[int]bool{}
	for seed := int64(0); seed < 20; seed++ {
		seen[len(GenerateScenario(seed).Workloads)] = true
	}
	if len(seen) < 2 {
		t.Error("scenario generation produced no variation across 20 seeds")
	}
}

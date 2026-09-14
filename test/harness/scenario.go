//go:build harness

package harness

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

type WorkloadSpec struct {
	Name            string
	Replicas        int
	EnforcedSpread  bool
	PDBMinAvailable int // 0 means no PDB
}

type Scenario struct {
	Seed         int64
	Zones        []string
	NodesPerZone int
	Workloads    []WorkloadSpec
}

// GenerateScenario builds a reproducible random cluster shape. The same seed
// always yields the same scenario, so a CI failure can be replayed locally by
// seed alone.
func GenerateScenario(seed int64) Scenario {
	r := rand.New(rand.NewSource(seed))

	zoneCount := 2 + r.Intn(3) // 2..4 zones
	zones := make([]string, zoneCount)
	for i := range zones {
		zones[i] = fmt.Sprintf("zone-%c", 'a'+i)
	}

	s := Scenario{
		Seed:         seed,
		Zones:        zones,
		NodesPerZone: 1 + r.Intn(3), // 1..3 nodes per zone
	}

	for i := 0; i < 1+r.Intn(4); i++ { // 1..4 workloads
		w := WorkloadSpec{
			Name:           fmt.Sprintf("app-%d", i),
			Replicas:       1 + r.Intn(5), // 1..5 replicas
			EnforcedSpread: r.Intn(2) == 0,
		}
		if r.Intn(2) == 0 {
			// Deliberately includes budgets that cannot be satisfied.
			w.PDBMinAvailable = 1 + r.Intn(w.Replicas+1)
		}
		s.Workloads = append(s.Workloads, w)
	}
	return s
}

// ApplyIn applies the scenario's workloads into a namespace. Nodes are
// cluster-scoped and therefore shared; only workloads are isolated.
func (s Scenario) ApplyIn(t *testing.T, c *Cluster, namespace string) {
	t.Helper()
	for _, z := range s.Zones {
		for i := 0; i < s.NodesPerZone; i++ {
			c.AddNode(t, fmt.Sprintf("n-%s-%d", z, i), z)
		}
	}
	for _, w := range s.Workloads {
		manifest := testDeployment(w.Name, w.Replicas)
		if w.EnforcedSpread {
			manifest = spreadDeployment(w.Name, w.Replicas)
		}
		c.Apply(t, withNamespace(manifest, namespace))
		if w.PDBMinAvailable > 0 {
			c.Apply(t, withNamespace(fmt.Sprintf(`
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata: {name: %s}
spec:
  minAvailable: %d
  selector: {matchLabels: {app: %s}}
`, w.Name, w.PDBMinAvailable, w.Name), namespace))
		}
	}
}

// TotalReplicas is how many pods the scenario should eventually run.
func (s Scenario) TotalReplicas() int {
	n := 0
	for _, w := range s.Workloads {
		n += w.Replicas
	}
	return n
}

// withNamespace injects a namespace into a rendered manifest's metadata.
func withNamespace(manifest, namespace string) string {
	return strings.Replace(manifest, "metadata: {name: ", "metadata: {namespace: "+namespace+", name: ", 1)
}

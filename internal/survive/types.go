package survive

import (
	"time"

	"github.com/SaiPisey2/kubectl-survive/internal/depgraph"
	"github.com/SaiPisey2/kubectl-survive/internal/pdbcheck"
	"github.com/SaiPisey2/kubectl-survive/internal/spread"
	"github.com/SaiPisey2/kubectl-survive/internal/volumepin"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
)

type Outcome string

const (
	OutcomeSurvives Outcome = "survives"
	OutcomeDegraded Outcome = "degraded"
	OutcomeLost     Outcome = "lost"
	OutcomeUnknown  Outcome = "unknown"
)

type Verdict struct {
	Workload     workload.Ref
	Outcome      Outcome
	Reason       string
	Placement    map[string]int
	Spread       spread.Assessment
	AntiAffinity spread.Assessment
	VolumePins   []volumepin.Pin

	// DependsOn names this workload's direct dependencies, resolved from
	// literal env var values naming a Service (spec §5.5, §8.4). It does
	// not vary by domain; it is populated here because JSON output is
	// shaped per (domain, workload) (spec §8.4's `dependsOn` field).
	DependsOn []string
}

type DomainResult struct {
	Domain   string
	Lost     int
	Degraded int
	// Impaired counts workloads whose own pods survive this domain's loss
	// but which transitively depend -- through Services -- on something
	// lost or unknown there (spec §5.5, ruling 1). It is a separate layer
	// from Lost/Degraded, which describe only the workload's own pods.
	Impaired    int
	Verdicts    []Verdict
	Impairments []depgraph.Impairment
}

type Report struct {
	TakenAt         time.Time
	DomainKey       string
	Domains         []DomainResult
	PDBFindings     []pdbcheck.Finding
	UnlabelledNodes []string
}

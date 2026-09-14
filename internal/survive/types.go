package survive

import (
	"time"

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
}

type DomainResult struct {
	Domain   string
	Lost     int
	Degraded int
	Verdicts []Verdict
}

type Report struct {
	TakenAt         time.Time
	DomainKey       string
	Domains         []DomainResult
	PDBFindings     []pdbcheck.Finding
	UnlabelledNodes []string
}

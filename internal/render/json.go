package render

import (
	"encoding/json"
	"io"

	"github.com/SaiPisey2/kubectl-survive/internal/draincheck"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
)

// jsonFinding is a reason a drain will hang, from either detector. Both are
// emitted because a machine consumer -- the exporter, or a CI gate -- has no
// other way to learn that a budget will block a drain, and omitting them made
// the JSON output say less than the table.
type jsonFinding struct {
	PDB       string `json:"pdb"`
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`             // "unsatisfiable" or "drainDeadlock"
	Domain    string `json:"domain,omitempty"` // set only for drainDeadlock
	Detail    string `json:"detail"`
}

type jsonWorkload struct {
	Namespace    string         `json:"namespace"`
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	Survives     bool           `json:"survives"`
	Outcome      string         `json:"outcome"`
	Reason       string         `json:"reason"`
	Placement    map[string]int `json:"placement"`
	Spread       string         `json:"spreadState"`
	AntiAffinity string         `json:"antiAffinity"`
	VolumePins   []string       `json:"volumePins,omitempty"`
}

type jsonDomain struct {
	Name      string         `json:"name"`
	Lost      int            `json:"workloadsLost"`
	Degraded  int            `json:"workloadsDegraded"`
	Workloads []jsonWorkload `json:"workloads"`
}

type jsonReport struct {
	APIVersion string       `json:"apiVersion"`
	SnapshotAt string       `json:"snapshotAt"`
	DomainKey  string       `json:"domainKey"`
	Domains    []jsonDomain `json:"domains"`
	Unlabelled []string     `json:"unlabelledNodes,omitempty"`
	// Findings carries both PDB satisfiability problems and drain deadlocks.
	// Deadlocks are absent, not empty, when the scheduler checks were disabled
	// by the version gate -- absence must not be read as "none found".
	Findings []jsonFinding `json:"drainFindings,omitempty"`
}

func JSON(w io.Writer, r *survive.Report, deadlocks ...draincheck.Finding) error {
	out := jsonReport{
		APIVersion: "survive.dev/v1alpha1",
		SnapshotAt: r.TakenAt.Format("2006-01-02T15:04:05Z"),
		DomainKey:  r.DomainKey,
		Unlabelled: r.UnlabelledNodes,
	}
	for _, f := range r.PDBFindings {
		out.Findings = append(out.Findings, jsonFinding{
			PDB:       f.PDB,
			Namespace: f.Namespace,
			Kind:      "unsatisfiable",
			Detail:    f.Detail,
		})
	}
	for _, f := range deadlocks {
		out.Findings = append(out.Findings, jsonFinding{
			PDB:       f.PDB,
			Namespace: f.Namespace,
			Kind:      "drainDeadlock",
			Domain:    f.Domain,
			Detail:    f.Detail,
		})
	}

	for _, d := range r.Domains {
		jd := jsonDomain{Name: d.Domain, Lost: d.Lost, Degraded: d.Degraded}
		for _, v := range d.Verdicts {
			var pins []string
			for _, pin := range v.VolumePins {
				pins = append(pins, pin.PV+"@"+pin.Domain)
			}
			jd.Workloads = append(jd.Workloads, jsonWorkload{
				Namespace:    v.Workload.Namespace,
				Name:         v.Workload.Name,
				Kind:         v.Workload.Kind,
				Survives:     v.Outcome == survive.OutcomeSurvives,
				Outcome:      string(v.Outcome),
				Reason:       v.Reason,
				Placement:    v.Placement,
				Spread:       string(v.Spread.State),
				AntiAffinity: string(v.AntiAffinity.State),
				VolumePins:   pins,
			})
		}
		out.Domains = append(out.Domains, jd)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

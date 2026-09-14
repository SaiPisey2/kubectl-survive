package render

import (
	"encoding/json"
	"io"

	"github.com/SaiPisey2/kubectl-survive/internal/survive"
)

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
}

func JSON(w io.Writer, r *survive.Report) error {
	out := jsonReport{
		APIVersion: "survive.dev/v1alpha1",
		SnapshotAt: r.TakenAt.Format("2006-01-02T15:04:05Z"),
		DomainKey:  r.DomainKey,
		Unlabelled: r.UnlabelledNodes,
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

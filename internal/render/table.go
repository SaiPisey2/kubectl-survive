package render

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/SaiPisey2/kubectl-survive/internal/survive"
)

// Table prints only the workloads that lose availability. A list of everything
// that is fine is not what an operator is reading for.
func Table(w io.Writer, r *survive.Report) error {
	fmt.Fprintf(w, "Domain key: %s   Snapshot %s\n\n", r.DomainKey, r.TakenAt.Format("2006-01-02T15:04:05Z"))

	for _, d := range r.Domains {
		fmt.Fprintf(w, "Losing %s  ->  %d lost, %d degraded\n", d.Domain, d.Lost, d.Degraded)

		// A domain with nothing wrong gets its summary line and nothing else.
		// A header with no rows under it reads as a truncated table rather than
		// as good news.
		if !anyAffected(d) {
			fmt.Fprintln(w)
			continue
		}

		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  WORKLOAD\tPLACEMENT\tVERDICT")
		for _, v := range d.Verdicts {
			if v.Outcome == survive.OutcomeSurvives {
				continue
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s %s\n", v.Workload.Name, placement(v.Placement), mark(v.Outcome), v.Reason)
			for _, pin := range v.VolumePins {
				fmt.Fprintf(tw, "      volume %s is pinned to %s\t\t\n", pin.PV, pin.Domain)
			}
		}
		tw.Flush()
		fmt.Fprintln(w)
	}

	for _, f := range r.PDBFindings {
		if f.Block != "" {
			fmt.Fprintf(w, "PDB %s/%s: %s\n", f.Namespace, f.PDB, f.Detail)
		}
	}
	if len(r.UnlabelledNodes) > 0 {
		fmt.Fprintf(w, "\n%d node(s) have no %s label; workloads on them are reported as unknown.\n",
			len(r.UnlabelledNodes), r.DomainKey)
	}
	return nil
}

func mark(o survive.Outcome) string {
	switch o {
	case survive.OutcomeLost:
		return "LOST"
	case survive.OutcomeDegraded:
		return "DEGRADED"
	case survive.OutcomeUnknown:
		return "UNKNOWN"
	default:
		return "OK"
	}
}

func placement(p map[string]int) string {
	var keys []string
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := ""
	for i, k := range keys {
		if i > 0 {
			s += " "
		}
		name := k
		if name == "" {
			name = "<no-zone>"
		}
		s += fmt.Sprintf("%s:%d", name, p[k])
	}
	return s
}

// anyAffected reports whether a domain has any verdict worth printing a table
// for.
func anyAffected(d survive.DomainResult) bool {
	for _, v := range d.Verdicts {
		if v.Outcome != survive.OutcomeSurvives {
			return true
		}
	}
	return false
}

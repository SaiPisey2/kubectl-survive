package render

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/SaiPisey2/kubectl-survive/internal/draincheck"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	"github.com/SaiPisey2/kubectl-survive/internal/workload"
)

// Table prints only the workloads that lose availability. A list of everything
// that is fine is not what an operator is reading for.
//
// deadlocks is variadic so every existing call site (and every fixture built
// before drain-deadlock detection existed) keeps compiling unchanged; pass
// nothing when the scheduler-backed check did not run (see sched.Gate).
func Table(w io.Writer, r *survive.Report, deadlocks ...draincheck.Finding) error {
	fmt.Fprintf(w, "Domain key: %s   Snapshot %s\n\n", r.DomainKey, r.TakenAt.Format("2006-01-02T15:04:05Z"))

	for _, d := range r.Domains {
		fmt.Fprintf(w, "Losing %s  ->  %d lost, %d degraded", d.Domain, d.Lost, d.Degraded)
		if d.Impaired > 0 {
			fmt.Fprintf(w, ", %d impaired by a dependency", d.Impaired)
		}
		fmt.Fprintln(w)

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
		// Impaired workloads are a separate layer from Lost/Degraded (spec
		// §5.5, ruling 1): their own pods survive, so they never appear in
		// the loop above, but an operator still needs to see why they are
		// named at all.
		// An impaired workload's own placement is the argument: it is usually
		// spread cleanly across every domain, and it still stops serving.
		// Its verdict in this domain is "survives", so the placement is there.
		placementOf := map[workload.Ref]map[string]int{}
		for _, v := range d.Verdicts {
			placementOf[v.Workload] = v.Placement
		}
		for _, imp := range d.Impairments {
			// imp.Chain starts with the workload's own name (spec §5.5's
			// "web -> session-store" example); it is dropped here because the
			// row is already labeled with it, so only the actual path to the
			// failing dependency is printed.
			rest := imp.Chain
			if len(rest) > 0 {
				rest = rest[1:]
			}
			fmt.Fprintf(tw, "  %s\t%s\tIMPAIRED depends on %s\n",
				imp.Workload.Name, placement(placementOf[imp.Workload]), chain(rest))
		}
		tw.Flush()
		fmt.Fprintln(w)
	}

	for _, f := range r.PDBFindings {
		if f.Block != "" {
			fmt.Fprintf(w, "PDB %s/%s: %s\n", f.Namespace, f.PDB, f.Detail)
		}
	}
	for _, f := range deadlocks {
		fmt.Fprintf(w, "PDB %s/%s: %s\n", f.Namespace, f.PDB, f.Detail)
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
	return d.Impaired > 0
}

func chain(c []string) string {
	return strings.Join(c, " -> ")
}

package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/SaiPisey2/kubectl-survive/internal/fix"
	"github.com/SaiPisey2/kubectl-survive/internal/verify"
)

// Fixes prints the verified remediation for every workload in rs that has
// something to say: a proven patch (spec 8.2) or, when no patch can help, the
// architectural finding that is the honest answer (spec 8.3). A workload with
// neither is skipped entirely -- there is nothing here for the operator to
// read.
func Fixes(w io.Writer, rs []verify.Result) error {
	for _, r := range rs {
		if len(r.Fixes) == 0 && len(r.Architectural) == 0 {
			continue
		}

		if len(r.Fixes) > 0 {
			fmt.Fprintf(w, "%s   ✗ dies with %s   (%s)\n\n", r.Workload.Name, r.Domain, r.Cause)
			for i, v := range r.Fixes {
				writeFixBlock(w, v, r.Domain)
				if i < len(r.Fixes)-1 {
					fmt.Fprintln(w)
				}
			}
		} else {
			fmt.Fprintf(w, "%s   ✗ dies with %s\n\n", r.Workload.Name, r.Domain)
			for i, af := range r.Architectural {
				writeArchitecturalBlock(w, af)
				if i < len(r.Architectural)-1 {
					fmt.Fprintln(w)
				}
			}
		}
		fmt.Fprintln(w)
	}
	return nil
}

// writeFixBlock prints one FIX or ALT entry. Spec 6.2 is explicit that a fix
// which schedules but does not make the workload survive must read as
// visibly partial: the label changes to ALT and the survives line carries an
// uppercase NO, never a quiet "no" easy to skim past.
func writeFixBlock(w io.Writer, v verify.Verified, domain string) {
	label := "FIX"
	if v.Partial {
		label = "ALT"
	}
	fmt.Fprintf(w, "  %s  %s\n", label, v.Fix.Title)

	schedulable := "no"
	if v.Proof.Schedulable {
		schedulable = "yes"
	}
	fmt.Fprintf(w, "       ├─ schedulable?  %s — %s\n", schedulable, v.Proof.SchedulableDetail)

	survives := "NO"
	if v.Proof.Survives {
		survives = "yes"
	}
	fmt.Fprintf(w, "       └─ survives %s?  %s — %s\n", domain, survives, v.Proof.SurvivesDetail)

	if v.Fix.Patch != "" {
		fmt.Fprintln(w)
		writePatch(w, v.Fix.Patch)
	}
}

// writePatch indents every line of a fix's YAML patch by two spaces, so the
// patch block reads as a nested part of the FIX/ALT entry above it rather
// than a top-level section.
func writePatch(w io.Writer, patch string) {
	lines := strings.Split(strings.TrimRight(patch, "\n"), "\n")
	for _, l := range lines {
		fmt.Fprintf(w, "  %s\n", l)
	}
}

// writeArchitecturalBlock prints spec 8.3's answer for a rung that can never
// be a patch: the cause, and the fact that this is an architecture change,
// not something a manifest edit can fix.
func writeArchitecturalBlock(w io.Writer, af fix.Fix) {
	fmt.Fprintf(w, "  Cause: %s\n\n", af.Architectural)
	fmt.Fprintln(w, "  This is an architecture change, not a patch:")
	fmt.Fprintln(w, "  replicate at the data layer, move to regional storage, or accept the risk")
	fmt.Fprintln(w, "  and document it.")
}

// jsonProof mirrors verify.Proof for the machine-readable form.
type jsonProof struct {
	Schedulable       bool   `json:"schedulable"`
	SchedulableDetail string `json:"schedulableDetail,omitempty"`
	Survives          bool   `json:"survives"`
	SurvivesDetail    string `json:"survivesDetail,omitempty"`
}

type jsonFix struct {
	Rung    int       `json:"rung"`
	Title   string    `json:"title"`
	Partial bool      `json:"partial"`
	Patch   string    `json:"patch,omitempty"`
	Proof   jsonProof `json:"proof"`
}

type jsonArchitectural struct {
	Rung          int    `json:"rung"`
	Title         string `json:"title"`
	Architectural string `json:"architectural"`
}

type jsonFixResult struct {
	Namespace     string              `json:"namespace"`
	Name          string              `json:"name"`
	Kind          string              `json:"kind"`
	Domain        string              `json:"domain"`
	Cause         string              `json:"cause,omitempty"`
	Fixes         []jsonFix           `json:"fixes,omitempty"`
	Architectural []jsonArchitectural `json:"architectural,omitempty"`
}

type jsonFixReport struct {
	APIVersion string          `json:"apiVersion"`
	Workloads  []jsonFixResult `json:"workloads"`
}

// FixesJSON emits the same information as Fixes in machine-readable form, one
// entry per workload that has a fix or an architectural finding to report.
func FixesJSON(w io.Writer, rs []verify.Result) error {
	out := jsonFixReport{APIVersion: "survive.dev/v1alpha1"}
	for _, r := range rs {
		if len(r.Fixes) == 0 && len(r.Architectural) == 0 {
			continue
		}
		jr := jsonFixResult{
			Namespace: r.Workload.Namespace,
			Name:      r.Workload.Name,
			Kind:      r.Workload.Kind,
			Domain:    r.Domain,
			Cause:     r.Cause,
		}
		for _, v := range r.Fixes {
			jr.Fixes = append(jr.Fixes, jsonFix{
				Rung:    int(v.Fix.Rung),
				Title:   v.Fix.Title,
				Partial: v.Partial,
				Patch:   v.Fix.Patch,
				Proof: jsonProof{
					Schedulable:       v.Proof.Schedulable,
					SchedulableDetail: v.Proof.SchedulableDetail,
					Survives:          v.Proof.Survives,
					SurvivesDetail:    v.Proof.SurvivesDetail,
				},
			})
		}
		for _, af := range r.Architectural {
			jr.Architectural = append(jr.Architectural, jsonArchitectural{
				Rung:          int(af.Rung),
				Title:         af.Title,
				Architectural: af.Architectural,
			})
		}
		out.Workloads = append(out.Workloads, jr)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

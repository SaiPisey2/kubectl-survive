package fix

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Input is everything the ladder needs to generate candidates for one
// workload. Candidates never consults the cluster: verification is a later
// task's job, which is what makes the ladder testable without a scheduler.
type Input struct {
	Verdict   survive.Verdict
	Template  *corev1.PodTemplateSpec
	Replicas  int
	DomainKey string
	Domains   []string          // every domain in the cluster, sorted
	Selector  map[string]string // the workload's identifying labels
}

// spreadForDomain returns the index of the topology spread constraint that
// applies to in.DomainKey, or -1 if none does. Topology spread constraints are
// a list; a workload can carry several for different topology keys, and only
// the one whose TopologyKey matches the domain under test is relevant.
func spreadForDomain(t *corev1.PodTemplateSpec, domainKey string) int {
	for i := range t.Spec.TopologySpreadConstraints {
		if t.Spec.TopologySpreadConstraints[i].TopologyKey == domainKey {
			return i
		}
	}
	return -1
}

// Candidates returns unverified fix candidates for in, in rung order.
func Candidates(in Input) []Fix {
	var out []Fix

	if f, ok := rung1SpreadAdd(in); ok {
		out = append(out, f)
	}
	if f, ok := rung2SpreadEnforce(in); ok {
		out = append(out, f)
	}
	if f, ok := rung3SpreadTighten(in); ok {
		out = append(out, f)
	}
	if f, ok := rung4ReplicasRaise(in); ok {
		out = append(out, f)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Rung < out[j].Rung })
	return out
}

// patchLine is one line of a unified-diff-style patch: a single leading
// marker byte (' ' for context, '+' for added, '-' for removed) followed by
// the YAML content at its natural indentation. Rendering this way — rather
// than interpolating a Go value into a format string — is what keeps every
// patch body real, appliable YAML: stripping the marker byte from every kept
// (' ' or '+') line reproduces the resulting document exactly.
type patchLine struct {
	marker byte
	text   string
}

func renderPatch(lines []patchLine) string {
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteByte(l.marker)
		sb.WriteString(l.text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// sortedKeys returns m's keys in sorted order, so rendered YAML never depends
// on Go's randomised map iteration order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// rung1SpreadAdd fires only when there are at least two replicas to spread,
// the workload has at least one identifying label, and no constraint exists
// yet for the domain key under test.
//
// A workload with no identifying labels is refused rather than given a
// constraint with an empty selector: an empty-but-present LabelSelector
// matches every pod in the namespace, not none, so a patch generated for one
// workload would silently constrain every unrelated pod sharing its
// namespace — a far more disruptive change than the one being recommended,
// and one the operator did not ask for.
func rung1SpreadAdd(in Input) (Fix, bool) {
	if in.Replicas < 2 {
		return Fix{}, false
	}
	if len(in.Selector) == 0 {
		return Fix{}, false
	}
	if spreadForDomain(in.Template, in.DomainKey) != -1 {
		return Fix{}, false
	}

	selector := in.Selector
	domainKey := in.DomainKey

	lines := []patchLine{
		{' ', "spec:"},
		{' ', "  template:"},
		{' ', "    spec:"},
		{' ', "      topologySpreadConstraints:"},
		{'+', "      - maxSkew: 1"},
		{'+', fmt.Sprintf("        topologyKey: %s", domainKey)},
		{'+', "        whenUnsatisfiable: DoNotSchedule"},
		{'+', "        labelSelector:"},
		{'+', "          matchLabels:"},
	}
	for _, k := range sortedKeys(selector) {
		lines = append(lines, patchLine{'+', fmt.Sprintf("            %s: %s", k, selector[k])})
	}

	return Fix{
		Rung:  RungSpreadAdd,
		Title: fmt.Sprintf("Add an enforced topology spread constraint on %s", domainKey),
		Mutate: func(t *corev1.PodTemplateSpec) {
			t.Spec.TopologySpreadConstraints = append(t.Spec.TopologySpreadConstraints, corev1.TopologySpreadConstraint{
				MaxSkew:           1,
				TopologyKey:       domainKey,
				WhenUnsatisfiable: corev1.DoNotSchedule,
				LabelSelector:     &metav1.LabelSelector{MatchLabels: selector},
			})
		},
		Patch: renderPatch(lines),
	}, true
}

// rung2SpreadEnforce fires only when a constraint for the domain key exists
// and is advisory. It rewrites that constraint in place and adds nothing.
func rung2SpreadEnforce(in Input) (Fix, bool) {
	idx := spreadForDomain(in.Template, in.DomainKey)
	if idx == -1 {
		return Fix{}, false
	}
	if in.Template.Spec.TopologySpreadConstraints[idx].WhenUnsatisfiable != corev1.ScheduleAnyway {
		return Fix{}, false
	}

	domainKey := in.DomainKey
	return Fix{
		Rung:  RungSpreadEnforce,
		Title: fmt.Sprintf("Enforce the existing topology spread constraint on %s", domainKey),
		Mutate: func(t *corev1.PodTemplateSpec) {
			i := spreadForDomain(t, domainKey)
			if i == -1 {
				return
			}
			t.Spec.TopologySpreadConstraints[i].WhenUnsatisfiable = corev1.DoNotSchedule
		},
		Patch: renderPatch([]patchLine{
			{' ', "spec:"},
			{' ', "  template:"},
			{' ', "    spec:"},
			{' ', "      topologySpreadConstraints:"},
			{' ', fmt.Sprintf("        - topologyKey: %s", domainKey)},
			{'-', "          whenUnsatisfiable: ScheduleAnyway"},
			{'+', "          whenUnsatisfiable: DoNotSchedule"},
		}),
	}, true
}

// rung3SpreadTighten fires only when a constraint for the domain key exists
// with a maxSkew loose enough to permit every replica in one domain. It is
// independent of rung 2: a weak advisory constraint can need both.
func rung3SpreadTighten(in Input) (Fix, bool) {
	idx := spreadForDomain(in.Template, in.DomainKey)
	if idx == -1 {
		return Fix{}, false
	}
	if in.Template.Spec.TopologySpreadConstraints[idx].MaxSkew <= 1 {
		return Fix{}, false
	}

	domainKey := in.DomainKey
	current := in.Template.Spec.TopologySpreadConstraints[idx].MaxSkew
	return Fix{
		Rung:  RungSpreadTighten,
		Title: fmt.Sprintf("Lower maxSkew to 1 on %s", domainKey),
		Mutate: func(t *corev1.PodTemplateSpec) {
			i := spreadForDomain(t, domainKey)
			if i == -1 {
				return
			}
			t.Spec.TopologySpreadConstraints[i].MaxSkew = 1
		},
		Patch: renderPatch([]patchLine{
			{' ', "spec:"},
			{' ', "  template:"},
			{' ', "    spec:"},
			{' ', "      topologySpreadConstraints:"},
			{' ', fmt.Sprintf("        - topologyKey: %s", domainKey)},
			{'-', fmt.Sprintf("          maxSkew: %d", current)},
			{'+', "          maxSkew: 1"},
		}),
	}, true
}

// rung4ReplicasRaise fires only when there are fewer replicas than domains.
// It carries no template mutation: the replica count lives on the workload,
// not the pod template. Task 10 treats a nil Mutate on a fixable rung as
// "simulate with a different replica count," using the Replicas field.
func rung4ReplicasRaise(in Input) (Fix, bool) {
	target := len(in.Domains)
	if in.Replicas >= target {
		return Fix{}, false
	}

	return Fix{
		Rung:     RungReplicasRaise,
		Title:    fmt.Sprintf("Raise replicas to %d to match the domain count", target),
		Mutate:   nil,
		Replicas: target,
		Patch: renderPatch([]patchLine{
			{' ', "spec:"},
			{'-', fmt.Sprintf("  replicas: %d", in.Replicas)},
			{'+', fmt.Sprintf("  replicas: %d", target)},
		}),
	}, true
}

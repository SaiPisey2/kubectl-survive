package fix

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SaiPisey2/kubectl-survive/internal/pdbcheck"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
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

	// PDB is the PodDisruptionBudget currently protecting this workload, or
	// nil when it has none. It drives rungs 5 and 6.
	PDB *policyv1.PodDisruptionBudget
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
	if f, ok := rung5PDBAdd(in); ok {
		out = append(out, f)
	}
	if f, ok := rung6PDBRepair(in); ok {
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
		Rung:                  RungSpreadAdd,
		Title:                 fmt.Sprintf("Add an enforced topology spread constraint on %s", domainKey),
		ImprovesSurvivability: true,
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
		Rung:                  RungSpreadEnforce,
		Title:                 fmt.Sprintf("Enforce the existing topology spread constraint on %s", domainKey),
		ImprovesSurvivability: true,
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
		Rung:                  RungSpreadTighten,
		Title:                 fmt.Sprintf("Lower maxSkew to 1 on %s", domainKey),
		ImprovesSurvivability: true,
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
		Rung:                  RungReplicasRaise,
		Title:                 fmt.Sprintf("Raise replicas to %d to match the domain count", target),
		Mutate:                nil,
		Replicas:              target,
		ImprovesSurvivability: true,
		Patch: renderPatch([]patchLine{
			{' ', "spec:"},
			{'-', fmt.Sprintf("  replicas: %d", in.Replicas)},
			{'+', fmt.Sprintf("  replicas: %d", target)},
		}),
	}, true
}

// pdbName derives a name for a PDB generated by rungs 5 and 6 from the
// workload's own name, falling back when the verdict carries none (a bare
// Input built by a test, for instance).
func pdbName(in Input) string {
	if in.Verdict.Workload.Name != "" {
		return in.Verdict.Workload.Name + "-pdb"
	}
	return "workload-pdb"
}

// rung5PDBAdd fires only when the workload has no PodDisruptionBudget at all
// and has at least two replicas to spare one during a drain.
//
// This carries exactly the same trap rung 1 was fixed to avoid: an empty
// LabelSelector on a PDB matches every pod in the namespace, and a budget
// that can never be satisfied hangs every drain that touches any of those
// pods, not just the one workload the operator meant to help. So, like
// rung 1, this refuses to fire without identifying labels rather than
// emitting a PDB with an empty-but-present selector.
//
// It never claims ImprovesSurvivability: a PDB is not consulted when a zone
// vanishes — its nodes are simply gone — so adding one changes nothing about
// whether this workload survives a domain loss. It only keeps a voluntary
// drain (a node cordon, a cluster upgrade) from hanging forever.
func rung5PDBAdd(in Input) (Fix, bool) {
	if in.PDB != nil {
		return Fix{}, false
	}
	if in.Replicas < 2 {
		return Fix{}, false
	}
	if len(in.Selector) == 0 {
		return Fix{}, false
	}

	selector := in.Selector
	name := pdbName(in)

	lines := []patchLine{
		{' ', "apiVersion: policy/v1"},
		{' ', "kind: PodDisruptionBudget"},
		{' ', "metadata:"},
		{'+', fmt.Sprintf("  name: %s", name)},
		{' ', "spec:"},
		{'+', "  maxUnavailable: 1"},
		{'+', "  selector:"},
		{'+', "    matchLabels:"},
	}
	for _, k := range sortedKeys(selector) {
		lines = append(lines, patchLine{'+', fmt.Sprintf("      %s: %s", k, selector[k])})
	}

	return Fix{
		Rung:                  RungPDBAdd,
		Title:                 "Add a PodDisruptionBudget with maxUnavailable: 1",
		ImprovesSurvivability: false,
		Patch:                 renderPatch(lines),
	}, true
}

// syntheticPDBSnapshot rebuilds just enough of a snapshot to ask pdbcheck
// whether in.PDB is satisfiable for in.Replicas pods carrying in.Selector.
// Reusing pdbcheck.Analyze here, rather than re-deriving the percentage
// rounding and evictable-pod arithmetic, is deliberate: two independent
// implementations of "can this budget ever be satisfied" would drift apart,
// and the tool would end up contradicting itself about the same PDB.
func syntheticPDBSnapshot(in Input) *snapshot.Snapshot {
	const ns = "default"

	pdb := in.PDB.DeepCopy()
	if pdb.Name == "" {
		pdb.Name = pdbName(in)
	}
	pdb.Namespace = ns
	// The satisfiability question depends only on the budget's own fields
	// against the replica count, not on whichever selector the caller's PDB
	// happens to carry, so the synthetic pods and the PDB selector are
	// aligned on in.Selector to guarantee the matched-pod count equals
	// in.Replicas.
	pdb.Spec.Selector = &metav1.LabelSelector{MatchLabels: in.Selector}

	pods := make([]*corev1.Pod, 0, in.Replicas)
	for i := 0; i < in.Replicas; i++ {
		pods = append(pods, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%d", pdb.Name, i),
				Namespace: ns,
				Labels:    in.Selector,
			},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			},
		})
	}

	return &snapshot.Snapshot{Pods: pods, PDBs: []*policyv1.PodDisruptionBudget{pdb}}
}

// pdbUnsatisfiable reports whether in.PDB, reconstructed against in.Replicas
// pods carrying in.Selector, is a budget pdbcheck.Analyze reports as never
// satisfiable.
func pdbUnsatisfiable(in Input) bool {
	if in.PDB == nil {
		return false
	}
	name := in.PDB.Name
	if name == "" {
		name = pdbName(in)
	}
	for _, finding := range pdbcheck.Analyze(syntheticPDBSnapshot(in)) {
		if finding.PDB == name {
			return finding.Block == pdbcheck.BlockNeverSatisfiable
		}
	}
	return false
}

// rung6PDBRepair fires when the workload's own budget is one pdbcheck
// already reports as unsatisfiable: no pod can ever be evicted under it, so
// any drain touching this workload hangs forever. It replaces the budget
// with a satisfiable maxUnavailable: 1, the same as rung 5 proposes for a
// workload that had none.
//
// Like rung 5, this never claims ImprovesSurvivability: repairing a budget
// unblocks drains, it does not make a single-zone workload survive a zone
// dying.
func rung6PDBRepair(in Input) (Fix, bool) {
	if in.PDB == nil {
		return Fix{}, false
	}
	if !pdbUnsatisfiable(in) {
		return Fix{}, false
	}

	name := in.PDB.Name
	if name == "" {
		name = pdbName(in)
	}

	var removed string
	switch {
	case in.PDB.Spec.MinAvailable != nil:
		removed = fmt.Sprintf("  minAvailable: %s", in.PDB.Spec.MinAvailable.String())
	case in.PDB.Spec.MaxUnavailable != nil:
		removed = fmt.Sprintf("  maxUnavailable: %s", in.PDB.Spec.MaxUnavailable.String())
	default:
		removed = "  {}"
	}

	return Fix{
		Rung:                  RungPDBRepair,
		Title:                 "Replace the unsatisfiable budget with maxUnavailable: 1",
		ImprovesSurvivability: false,
		Patch: renderPatch([]patchLine{
			{' ', "apiVersion: policy/v1"},
			{' ', "kind: PodDisruptionBudget"},
			{' ', "metadata:"},
			{' ', fmt.Sprintf("  name: %s", name)},
			{' ', "spec:"},
			{'-', removed},
			{'+', "  maxUnavailable: 1"},
		}),
	}, true
}

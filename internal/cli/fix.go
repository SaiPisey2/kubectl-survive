package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/kubernetes"

	"github.com/SaiPisey2/kubectl-survive/internal/render"
	"github.com/SaiPisey2/kubectl-survive/internal/sched"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	"github.com/SaiPisey2/kubectl-survive/internal/verify"
)

// FixOptions holds everything the fix subcommand needs to run. --apply and
// --pr are out of scope for this milestone: the command stays read-only,
// either printing patches or writing them to --out-dir.
type FixOptions struct {
	ConfigFlags *genericclioptions.ConfigFlags
	Streams     genericiooptions.IOStreams

	DomainKey string
	Output    string
	OutDir    string
	Only      []string
}

func newFixCmd(streams genericiooptions.IOStreams) *cobra.Command {
	o := &FixOptions{
		ConfigFlags: genericclioptions.NewConfigFlags(true),
		Streams:     streams,
	}

	cmd := &cobra.Command{
		Use:   "fix [workload...]",
		Short: "Show verified remediation for workloads that lose availability",
		Long: "Show verified remediation for workloads that lose availability.\n\n" +
			"Every fix printed here has been checked twice: the real scheduler accepts\n" +
			"the mutated pod on this cluster's actual nodes, and the survivability\n" +
			"computation, re-run on that placement, no longer reports the workload\n" +
			"lost. A fix that helps without solving the problem is still printed, but\n" +
			"labelled ALT and marked as not surviving.\n\n" +
			"This command is read-only: it prints patches, or writes them to a\n" +
			"directory with --out-dir. It never touches the cluster.",
		RunE: func(cmd *cobra.Command, args []string) error {
			o.Only = args
			return o.Run(cmd.Context())
		},
	}

	o.ConfigFlags.AddFlags(cmd.Flags())
	cmd.Flags().StringVar(&o.DomainKey, "domain-key", "topology.kubernetes.io/zone",
		"node label defining the failure domain")
	cmd.Flags().StringVarP(&o.Output, "output", "o", "table", "output format: table|json")
	cmd.Flags().StringVar(&o.OutDir, "out-dir", "",
		"write one patch file per fix to this directory instead of printing them")
	return cmd
}

// Run fetches the cluster's state, applies the version gate, and either
// prints or writes the verified fixes.
func (o *FixOptions) Run(ctx context.Context) error {
	cfg, err := o.ConfigFlags.ToRESTConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}

	snap, err := snapshot.Fetch(ctx, cs)
	if err != nil {
		return err
	}

	// A discovery error is treated exactly like a version mismatch: unknown
	// is never assumed to match, so it disables the scheduler-backed checks
	// rather than guessing they would pass.
	serverVersion := ""
	if info, verErr := cs.Discovery().ServerVersion(); verErr == nil {
		serverVersion = info.GitVersion
	}
	gate := sched.Gate(serverVersion)

	return o.runWithSnapshot(ctx, snap, gate)
}

// runWithSnapshot is Run's cluster-independent core: given an already-built
// snapshot and version gate, it decides whether fix verification may run at
// all, and if so, renders or writes the result. Splitting this out is what
// makes the version-gate behaviour testable without a live cluster.
func (o *FixOptions) runWithSnapshot(ctx context.Context, snap *snapshot.Snapshot, gate sched.VersionGate) error {
	report := survive.Analyze(snap, o.DomainKey)

	if !gate.Enabled {
		// Spec 7.2: survivability is version-independent and still runs; only
		// the schedulability proof degrades, and it says so rather than going
		// silent on exactly the clusters that most need it.
		fmt.Fprintln(o.Streams.ErrOut, gate.Warning)
		return o.renderReport(report)
	}

	results, err := verify.Analyze(ctx, snap, report, o.DomainKey, o.Only)
	if err != nil {
		return err
	}

	if o.OutDir != "" {
		return writeFixPatches(o.OutDir, results)
	}
	return o.renderFixes(results)
}

func (o *FixOptions) renderReport(report *survive.Report) error {
	switch o.Output {
	case "json":
		return render.JSON(o.Streams.Out, report)
	case "table", "":
		return render.Table(o.Streams.Out, report)
	default:
		return fmt.Errorf("unsupported output format %q (want table or json)", o.Output)
	}
}

func (o *FixOptions) renderFixes(results []verify.Result) error {
	switch o.Output {
	case "json":
		return render.FixesJSON(o.Streams.Out, results)
	case "table", "":
		return render.Fixes(o.Streams.Out, results)
	default:
		return fmt.Errorf("unsupported output format %q (want table or json)", o.Output)
	}
}

// writeFixPatches writes one file per verified, non-architectural fix to
// dir, named "<namespace>-<name>-rung<N>.yaml". Workload names come from the
// cluster and are not trustworthy as path components, so both are reduced to
// their final path element with filepath.Base and rejected outright if a
// separator still survives that -- a name built to look like "../../etc"
// must never place a file outside dir.
func writeFixPatches(dir string, results []verify.Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving output directory: %w", err)
	}

	for _, r := range results {
		for _, v := range r.Fixes {
			ns, err := safePathComponent(r.Workload.Namespace)
			if err != nil {
				return fmt.Errorf("workload namespace %q: %w", r.Workload.Namespace, err)
			}
			name, err := safePathComponent(r.Workload.Name)
			if err != nil {
				return fmt.Errorf("workload name %q: %w", r.Workload.Name, err)
			}

			filename := fmt.Sprintf("%s-%s-rung%d.yaml", ns, name, v.Fix.Rung)
			path := filepath.Join(dir, filename)

			absPath, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolving patch path: %w", err)
			}
			if !withinDir(absDir, absPath) {
				return fmt.Errorf("refusing to write patch outside %s: %s", dir, path)
			}

			if err := os.WriteFile(path, []byte(v.Fix.Patch), 0o644); err != nil {
				return fmt.Errorf("writing patch file %s: %w", path, err)
			}
		}
	}
	return nil
}

// safePathComponent reduces name to its final path element with
// filepath.Base and refuses it outright, rather than silently substituting
// the reduced form, whenever that changes anything: a workload name that
// needed reducing was built (deliberately or not) to look like a path, and
// guessing what the operator meant by it is exactly the "unknown reported as
// safe" mistake this tool refuses to make elsewhere.
func safePathComponent(name string) (string, error) {
	base := filepath.Base(name)
	if base != name || base == "" || base == "." || base == ".." {
		return "", fmt.Errorf("unsafe path component %q", name)
	}
	if strings.ContainsRune(base, filepath.Separator) || strings.ContainsRune(base, '/') {
		return "", fmt.Errorf("contains a path separator after sanitisation")
	}
	return base, nil
}

// withinDir reports whether absPath is absDir itself or lives inside it.
func withinDir(absDir, absPath string) bool {
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

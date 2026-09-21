package cli

import (
	"context"
	"fmt"

	"github.com/SaiPisey2/kubectl-survive/internal/draincheck"
	"github.com/SaiPisey2/kubectl-survive/internal/render"
	"github.com/SaiPisey2/kubectl-survive/internal/sched"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	"github.com/SaiPisey2/kubectl-survive/internal/survive"
	"k8s.io/client-go/kubernetes"
)

func (o *Options) Run(ctx context.Context) error {
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
	// rather than guessing they would pass. Mirrors FixOptions.Run.
	serverVersion := ""
	if info, verErr := cs.Discovery().ServerVersion(); verErr == nil {
		serverVersion = info.GitVersion
	}
	gate := sched.Gate(serverVersion)

	return o.runWithSnapshot(ctx, snap, gate)
}

// runWithSnapshot is Run's cluster-independent core, split out so the
// version-gate behaviour is testable without a live cluster (see
// FixOptions.runWithSnapshot, which follows the same shape).
func (o *Options) runWithSnapshot(ctx context.Context, snap *snapshot.Snapshot, gate sched.VersionGate) error {
	report := survive.Analyze(snap, o.DomainKey)

	if !gate.Enabled {
		// Spec 7.2: survivability analysis is version-independent and keeps
		// working when scheduler-backed checks are disabled. Domain verdicts
		// above are already computed without the scheduler; drain-deadlock
		// detection is the one scheduler-backed check this command has, so it
		// is the one that is skipped here -- named, not silently dropped.
		fmt.Fprintln(o.Streams.ErrOut, gate.Warning)
		return o.render(report, nil)
	}

	sc, err := sched.New(ctx, snap)
	if err != nil {
		return fmt.Errorf("build scheduler: %w", err)
	}
	deadlocks, err := draincheck.Detect(ctx, sc, snap, o.DomainKey)
	if err != nil {
		return fmt.Errorf("drain-deadlock detection: %w", err)
	}

	return o.render(report, deadlocks)
}

func (o *Options) render(report *survive.Report, deadlocks []draincheck.Finding) error {
	switch o.Output {
	case "json":
		return render.JSON(o.Streams.Out, report, deadlocks...)
	case "table":
		return render.Table(o.Streams.Out, report, deadlocks...)
	default:
		return fmt.Errorf("unsupported output format %q (want table or json)", o.Output)
	}
}

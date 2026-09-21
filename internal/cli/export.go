package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/kubernetes"

	"github.com/SaiPisey2/kubectl-survive/internal/exporter"
	"github.com/SaiPisey2/kubectl-survive/internal/sched"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
)

// ExportOptions holds everything the export subcommand needs to run. It is
// the same cluster-independent split as Options and FixOptions: Run builds
// the real clientset and Fetcher, and hands off to the exporter package,
// which is what carries all the logic tests actually exercise.
type ExportOptions struct {
	ConfigFlags *genericclioptions.ConfigFlags
	Streams     genericiooptions.IOStreams

	DomainKey       string
	Listen          string
	Interval        time.Duration
	AnalysisTimeout time.Duration
}

func newExportCmd(streams genericiooptions.IOStreams) *cobra.Command {
	o := &ExportOptions{
		ConfigFlags: genericclioptions.NewConfigFlags(true),
		Streams:     streams,
	}

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Serve survivability findings as Prometheus metrics on an interval",
		Long: "Serve survivability findings as Prometheus metrics on an interval.\n\n" +
			"Survivability decays: nothing changes in git, a node drains, and a\n" +
			"workload that used to spread across zones no longer does. A command\n" +
			"someone has to remember to run cannot catch that; a scrape can. This\n" +
			"re-runs the same read-only analysis as the default command, on a timer,\n" +
			"off the HTTP goroutine, and serves it at /metrics.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.Run(cmd.Context())
		},
	}

	o.ConfigFlags.AddFlags(cmd.Flags())
	cmd.Flags().StringVar(&o.DomainKey, "domain-key", "topology.kubernetes.io/zone",
		"node label defining the failure domain")
	cmd.Flags().StringVar(&o.Listen, "listen", ":9090", "address to serve /metrics on")
	cmd.Flags().DurationVar(&o.Interval, "interval", 60*time.Second,
		"how often to re-run the analysis. A full snapshot fetch plus analysis is "+
			"not free (the drain-verification harness measured single scenarios in "+
			"the 6-25s range); 60s stays well clear of that while still catching a "+
			"drain-induced regression within a minute of it happening")
	cmd.Flags().DurationVar(&o.AnalysisTimeout, "analysis-timeout", 2*time.Minute,
		"maximum time a single analysis cycle may take before it is abandoned and reported as a scrape error")
	return cmd
}

// Run builds the cluster client and Fetcher, then blocks running the
// exporter's HTTP server and its background analysis loop until the
// command's context is cancelled.
func (o *ExportOptions) Run(ctx context.Context) error {
	cfg, err := o.ConfigFlags.ToRESTConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}

	fetch := func(ctx context.Context) (*snapshot.Snapshot, sched.VersionGate, error) {
		snap, err := snapshot.Fetch(ctx, cs)
		if err != nil {
			return nil, sched.VersionGate{}, err
		}

		// Same rule as run.go and fix.go: an unreadable server version is
		// never assumed to match, so it disables the scheduler-backed check
		// rather than guessing it would pass.
		serverVersion := ""
		if info, verErr := cs.Discovery().ServerVersion(); verErr == nil {
			serverVersion = info.GitVersion
		}
		gate := sched.Gate(serverVersion)
		if !gate.Enabled {
			fmt.Fprintln(o.Streams.ErrOut, gate.Warning)
		}
		return snap, gate, nil
	}

	e := exporter.New(o.DomainKey)

	mux := http.NewServeMux()
	mux.Handle("/metrics", e.Handler())
	srv := &http.Server{Addr: o.Listen, Handler: mux}

	serveErr := make(chan error, 1)
	go func() {
		fmt.Fprintf(o.Streams.Out, "serving /metrics on %s (interval %s)\n", o.Listen, o.Interval)
		serveErr <- srv.ListenAndServe()
	}()

	loopErr := make(chan error, 1)
	go func() { loopErr <- e.Run(ctx, fetch, o.Interval, o.AnalysisTimeout) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutting down metrics server: %w", err)
		}
		<-loopErr
		return nil
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("metrics server: %w", err)
		}
		return nil
	}
}

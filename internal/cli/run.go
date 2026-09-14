package cli

import (
	"context"
	"fmt"

	"github.com/SaiPisey2/kubectl-survive/internal/render"
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
	report := survive.Analyze(snap, o.DomainKey)

	switch o.Output {
	case "json":
		return render.JSON(o.Streams.Out, report)
	case "table":
		return render.Table(o.Streams.Out, report)
	default:
		return fmt.Errorf("unsupported output format %q (want table or json)", o.Output)
	}
}

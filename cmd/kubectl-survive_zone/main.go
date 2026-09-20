package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/SaiPisey2/kubectl-survive/internal/cli"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	// Lets the plugin authenticate against GKE/EKS/AKS kubeconfigs.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	streams := genericiooptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}
	cmd := cli.NewCmd(streams)
	cmd.SetErr(streams.ErrOut)
	if err := cmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

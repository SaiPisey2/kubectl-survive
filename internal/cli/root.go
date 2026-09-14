package cli

import (
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
)

// Options holds everything the command needs to run.
type Options struct {
	ConfigFlags *genericclioptions.ConfigFlags
	Streams     genericiooptions.IOStreams

	DomainKey string
	Output    string
}

func NewCmd(streams genericiooptions.IOStreams) *cobra.Command {
	o := &Options{
		ConfigFlags: genericclioptions.NewConfigFlags(true),
		Streams:     streams,
	}

	cmd := &cobra.Command{
		Use:   "survive",
		Short: "Report which workloads lose availability if a failure domain is lost",
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.Run(cmd.Context())
		},
	}

	// Supplies --kubeconfig, --context, --namespace and friends.
	o.ConfigFlags.AddFlags(cmd.Flags())
	cmd.Flags().StringVar(&o.DomainKey, "domain-key", "topology.kubernetes.io/zone",
		"node label defining the failure domain")
	cmd.Flags().StringVarP(&o.Output, "output", "o", "table", "output format: table|json")
	return cmd
}

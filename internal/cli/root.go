package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/SaiPisey2/kubectl-survive/internal/version"
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
		Use:     "survive-zone",
		Short:   "Report which workloads lose availability if a failure domain is lost",
		Version: version.Get().String(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.Run(cmd.Context())
		},
	}

	// Supplies --kubeconfig, --context, --namespace and friends.
	o.ConfigFlags.AddFlags(cmd.Flags())
	cmd.Flags().StringVar(&o.DomainKey, "domain-key", "topology.kubernetes.io/zone",
		"node label defining the failure domain")
	cmd.Flags().StringVarP(&o.Output, "output", "o", "table", "output format: table|json")

	// A failure to reach the cluster is not a usage error. Printing the whole
	// flag list after one is noise that buries the actual message.
	cmd.SilenceUsage = true

	// Plain identity line rather than cobra's "survive version <x>" default.
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.AddCommand(newVersionCmd(streams))
	cmd.AddCommand(newFixCmd(streams))
	return cmd
}

func newVersionCmd(streams genericiooptions.IOStreams) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the build identity of this binary",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			info := version.Get()
			switch output {
			case "json":
				enc := json.NewEncoder(streams.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			case "table", "":
				_, err := fmt.Fprintln(streams.Out, info.String())
				return err
			default:
				return fmt.Errorf("unsupported output format %q: use table or json", output)
			}
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table|json")
	return cmd
}

package cli

import (
	"bytes"
	"testing"

	"k8s.io/cli-runtime/pkg/genericiooptions"
)

func TestExportSubcommandHasExpectedFlags(t *testing.T) {
	streams := genericiooptions.IOStreams{In: &bytes.Buffer{}, Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}
	cmd := NewCmd(streams)

	export, _, err := cmd.Find([]string{"export"})
	if err != nil {
		t.Fatalf("export subcommand not registered: %v", err)
	}
	for _, f := range []string{"kubeconfig", "context", "namespace", "domain-key", "listen", "interval", "analysis-timeout"} {
		if export.Flags().Lookup(f) == nil {
			t.Errorf("missing flag %q on export", f)
		}
	}
}

func TestExportListenAndIntervalDefaults(t *testing.T) {
	streams := genericiooptions.IOStreams{In: &bytes.Buffer{}, Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}
	cmd := NewCmd(streams)
	export, _, err := cmd.Find([]string{"export"})
	if err != nil {
		t.Fatalf("export subcommand not registered: %v", err)
	}

	if got := export.Flags().Lookup("listen").DefValue; got != ":9090" {
		t.Errorf("--listen default = %q, want %q", got, ":9090")
	}
	if got := export.Flags().Lookup("interval").DefValue; got != "1m0s" {
		t.Errorf("--interval default = %q, want %q", got, "1m0s")
	}
}

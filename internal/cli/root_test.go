package cli

import (
	"bytes"
	"testing"

	"k8s.io/cli-runtime/pkg/genericiooptions"
)

func TestNewCmdHasExpectedFlags(t *testing.T) {
	streams := genericiooptions.IOStreams{In: &bytes.Buffer{}, Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}
	cmd := NewCmd(streams)

	if cmd.Use != "survive" {
		t.Errorf("Use = %q, want %q", cmd.Use, "survive")
	}
	for _, f := range []string{"kubeconfig", "context", "namespace", "domain-key", "output"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("missing flag %q", f)
		}
	}
}

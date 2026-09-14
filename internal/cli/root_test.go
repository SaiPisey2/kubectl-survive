package cli

import (
	"bytes"
	"encoding/json"
	"strings"
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

func TestVersionSubcommandPrintsIdentity(t *testing.T) {
	out := &bytes.Buffer{}
	streams := genericiooptions.IOStreams{In: &bytes.Buffer{}, Out: out, ErrOut: &bytes.Buffer{}}
	cmd := NewCmd(streams)
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.HasPrefix(out.String(), "kubectl-survive ") {
		t.Fatalf("unexpected output %q", out.String())
	}
}

func TestVersionSubcommandJSONIsParseable(t *testing.T) {
	out := &bytes.Buffer{}
	streams := genericiooptions.IOStreams{In: &bytes.Buffer{}, Out: out, ErrOut: &bytes.Buffer{}}
	cmd := NewCmd(streams)
	cmd.SetArgs([]string{"version", "-o", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version -o json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v (%q)", err, out.String())
	}
	for _, k := range []string{"version", "goVersion", "platform"} {
		if got[k] == "" || got[k] == nil {
			t.Errorf("field %q missing or empty in %v", k, got)
		}
	}
}

func TestVersionSubcommandRejectsUnknownFormat(t *testing.T) {
	streams := genericiooptions.IOStreams{In: &bytes.Buffer{}, Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}
	cmd := NewCmd(streams)
	cmd.SetArgs([]string{"version", "-o", "yaml"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("an unsupported format must be an error, never a silent fallback")
	}
}

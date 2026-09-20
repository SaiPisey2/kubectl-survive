package version

import (
	"strings"
	"testing"
)

func TestGetFallsBackWhenNotInjected(t *testing.T) {
	i := Get()
	if i.Version == "" {
		t.Fatal("version must never be empty; a build with no ldflags still reports something")
	}
	if i.GoVersion == "" || i.Platform == "" {
		t.Fatalf("runtime facts missing: %+v", i)
	}
	if !strings.Contains(i.Platform, "/") {
		t.Fatalf("platform must be os/arch, got %q", i.Platform)
	}
}

func TestStringShortensCommitAndNamesBinary(t *testing.T) {
	i := Info{
		Version:   "v1.2.3",
		Commit:    "0123456789abcdef",
		Date:      "2026-09-14T00:00:00Z",
		GoVersion: "go1.26.5",
		Platform:  "linux/amd64",
	}
	got := i.String()
	if !strings.HasPrefix(got, "kubectl-survive_zone v1.2.3 (0123456) built 2026-09-14T00:00:00Z") {
		t.Fatalf("unexpected rendering: %q", got)
	}
	if strings.Contains(got, "0123456789abcdef") {
		t.Fatalf("commit must be shortened to 7 characters: %q", got)
	}
	if !strings.Contains(got, "go1.26.5 linux/amd64") {
		t.Fatalf("runtime facts missing: %q", got)
	}
}

func TestStringOmitsUnknownFields(t *testing.T) {
	i := Info{Version: "devel", GoVersion: "go1.26.5", Platform: "darwin/arm64"}
	got := i.String()
	if strings.Contains(got, "(") || strings.Contains(got, "built") {
		t.Fatalf("absent commit and date must not render as empty decoration: %q", got)
	}
}

func TestGetDoesNotDoubleSuffixInjectedVersion(t *testing.T) {
	t.Cleanup(func() { version = "" })
	version = "v0.1.0-dirty"
	if got := Get().Version; got != "v0.1.0-dirty" {
		t.Fatalf("an ldflags version is authoritative and must not gain a second suffix, got %q", got)
	}
}

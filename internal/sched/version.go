package sched

import (
	"fmt"
	"regexp"
)

// TargetMinor is the Kubernetes minor this build's scheduler framework came
// from. Framework types moved packages in 1.34 and again in 1.35, so a build
// serves exactly one minor and each future minor is a deliberate migration.
const TargetMinor = "1.35"

// minorRE requires a trailing dot after the minor, i.e. a patch component
// must follow. A GitVersion with no patch at all (e.g. "v1.35") is not
// shaped like any real server version - every apiserver/kubelet build
// reports at least a patch number - so it is treated the same as an
// unreadable version rather than guessed to mean the target minor.
var minorRE = regexp.MustCompile(`^v?(\d+\.\d+)\.`)

// VersionGate says whether the scheduler-backed checks may run against this
// server, and carries the message to print when they may not.
type VersionGate struct {
	Server  string
	Target  string
	Enabled bool
	Warning string
}

// Gate compares the server's reported version against this build's target.
// Anything it cannot read disables the checks: an unreadable version is not
// evidence of a match.
func Gate(serverGitVersion string) VersionGate {
	g := VersionGate{Server: serverGitVersion, Target: TargetMinor}

	m := minorRE.FindStringSubmatch(serverGitVersion)
	if m != nil && m[1] == TargetMinor {
		g.Enabled = true
		return g
	}

	shown := serverGitVersion
	if shown == "" {
		shown = "unknown"
	}
	g.Warning = fmt.Sprintf(
		"Warning: this build targets Kubernetes %s; cluster reports %s.\n"+
			"  Survivability analysis: OK (version-independent).\n"+
			"  Fix verification:       DISABLED - cannot confirm fixes still schedule.",
		TargetMinor, shown)
	return g
}

package sched

import (
	"strings"
	"testing"
)

func TestGateEnablesOnTheTargetMinor(t *testing.T) {
	for _, v := range []string{"v1.35.0", "v1.35.7", "v1.35.7-eks-a1b2c3", "v1.35.12+k3s1"} {
		g := Gate(v)
		if !g.Enabled {
			t.Errorf("%s is the target minor and must enable the scheduler checks: %+v", v, g)
		}
		if g.Warning != "" {
			t.Errorf("%s must not warn: %q", v, g.Warning)
		}
	}
}

func TestGateDisablesOnAnyOtherMinor(t *testing.T) {
	for _, v := range []string{"v1.31.6", "v1.34.9", "v1.36.1"} {
		g := Gate(v)
		if g.Enabled {
			t.Errorf("%s is not the target minor and must disable fix verification", v)
		}
		if !strings.Contains(g.Warning, v) || !strings.Contains(g.Warning, TargetMinor) {
			t.Errorf("the warning must name both versions, got %q", g.Warning)
		}
		if !strings.Contains(g.Warning, "Survivability analysis: OK") {
			t.Errorf("the warning must say survivability still runs, got %q", g.Warning)
		}
	}
}

func TestGateDisablesWhenTheVersionIsUnreadable(t *testing.T) {
	// An unparseable version is not evidence of a match. Unknown is never safe.
	for _, v := range []string{"", "garbage", "v1"} {
		if g := Gate(v); g.Enabled {
			t.Errorf("an unreadable version %q must not enable the scheduler checks", v)
		}
	}
}

// TestGateHandlesVersionWithoutVPrefix covers clients that report GitVersion
// without the leading "v" (some vendored/mocked discovery clients and older
// distro builds do this, even though upstream kubelet/kube-apiserver always
// include it). The regexp's "v?" must not be dead code.
func TestGateHandlesVersionWithoutVPrefix(t *testing.T) {
	g := Gate("1.35.7")
	if !g.Enabled {
		t.Errorf("1.35.7 without a v prefix is still the target minor: %+v", g)
	}
	if g.Warning != "" {
		t.Errorf("must not warn: %q", g.Warning)
	}
}

// TestGateDisablesOnBareMinorWithNoPatch documents a deliberate choice: a
// GitVersion with no patch component at all (no trailing "." after the
// minor) is not shaped like any real server version — every real
// apiserver/kubelet build reports at least a patch number, even if it is
// "0" or a "+build" suffix follows it. Since guessing is worse than
// silence, a bare "v1.35" is treated the same as an unreadable version:
// disabled, not assumed to mean 1.35.0.
func TestGateDisablesOnBareMinorWithNoPatch(t *testing.T) {
	if g := Gate("v1.35"); g.Enabled {
		t.Errorf("a bare minor with no patch component must not enable the scheduler checks: %+v", g)
	}
}

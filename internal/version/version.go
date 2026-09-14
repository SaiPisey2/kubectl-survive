// Package version reports the build identity of the binary.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Injected at release time with -ldflags. A source build leaves them empty and
// falls back to the module's build information.
var (
	version = ""
	commit  = ""
	date    = ""
)

// Info describes the running binary.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// Get resolves the build identity, preferring ldflags and falling back to the
// module information the toolchain embeds in `go install` builds.
func Get() Info {
	i := Info{
		Version:   version,
		Commit:    commit,
		Date:      date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}

	// A version supplied by ldflags is authoritative: `git describe --dirty`
	// already carries its own suffix, so build information must not re-add one.
	injected := version != ""

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		if i.Version == "" {
			i.Version = "unknown"
		}
		return i
	}

	if i.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		i.Version = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if i.Commit == "" {
				i.Commit = s.Value
			}
		case "vcs.time":
			if i.Date == "" {
				i.Date = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" && !injected && !strings.HasSuffix(i.Version, "-dirty") {
				i.Version += "-dirty"
			}
		}
	}
	if i.Version == "" {
		i.Version = "devel"
	}
	return i
}

// String renders a single human-readable line.
func (i Info) String() string {
	s := i.Version
	if i.Commit != "" {
		c := i.Commit
		if len(c) > 7 {
			c = c[:7]
		}
		s += " (" + c + ")"
	}
	if i.Date != "" {
		s += " built " + i.Date
	}
	return fmt.Sprintf("kubectl-survive %s, %s %s", s, i.GoVersion, i.Platform)
}

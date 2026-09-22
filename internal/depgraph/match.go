package depgraph

import (
	"net/url"
	"regexp"
	"strings"
)

// hostLabel is one DNS label: RFC 1123, alphanumeric with internal hyphens.
const hostLabel = `[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?`

var (
	hostnameRe = regexp.MustCompile(`^` + hostLabel + `(\.` + hostLabel + `)*$`)
	hostPortRe = regexp.MustCompile(`^(` + hostLabel + `(\.` + hostLabel + `)*):([0-9]{1,5})$`)
)

// extractHostCandidate pulls a candidate hostname out of a literal env var
// value, and reports whether that hostname was found in a "host position" --
// a URL host, or the host part of host:port -- as opposed to simply being
// the entire value with nothing to disambiguate it from an arbitrary word.
//
// A dotted (multi-label) value found as the whole string is treated as
// proven too: unlike a single bare word, a dotted name cannot plausibly be a
// mode, a log level, or any other non-hostname config value.
func extractHostCandidate(value string) (host string, proven bool, ok bool) {
	v := strings.TrimSpace(value)
	if v == "" || v != value {
		// Leading/trailing space is never present in a real hostname or URL;
		// reject rather than guess at what the operator meant.
		return "", false, false
	}

	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" {
			return "", false, false
		}
		return u.Hostname(), true, true
	}

	if m := hostPortRe.FindStringSubmatch(v); m != nil {
		return m[1], true, true
	}

	if hostnameRe.MatchString(v) {
		return v, strings.Contains(v, "."), true
	}

	return "", false, false
}

// matchService decides whether a candidate host names a Service that
// actually exists in the snapshot, per the conservative rules of spec §5.5
// ruling 2:
//
//   - "svc.ns", "svc.ns.svc" and "svc.ns.svc.<cluster-domain>" (any suffix
//     after ".svc.") are accepted for any namespace, because the dotted,
//     namespace-qualified shape is unambiguous.
//   - the bare short name "svc" is accepted only when the workload is in
//     the Service's own namespace AND the name was found in a proven host
//     position (a URL host, or host:port) -- never from a bare word alone.
func matchService(host string, proven bool, workloadNamespace string, exists func(namespace, name string) bool) (ServiceKey, bool) {
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")

	switch {
	case len(labels) == 1:
		if !proven {
			return ServiceKey{}, false
		}
		if exists(workloadNamespace, labels[0]) {
			return ServiceKey{Namespace: workloadNamespace, Name: labels[0]}, true
		}
	case len(labels) == 2:
		name, ns := labels[0], labels[1]
		if exists(ns, name) {
			return ServiceKey{Namespace: ns, Name: name}, true
		}
	default: // len(labels) >= 3: "svc.ns.svc[.<cluster-domain...>]"
		name, ns, third := labels[0], labels[1], labels[2]
		if third == "svc" && exists(ns, name) {
			return ServiceKey{Namespace: ns, Name: name}, true
		}
	}
	return ServiceKey{}, false
}

// matchServiceRef is the single entry point Build uses: extract a host
// candidate from a literal env value, then match it against known Services.
func matchServiceRef(value, workloadNamespace string, exists func(namespace, name string) bool) (ServiceKey, bool) {
	host, proven, ok := extractHostCandidate(value)
	if !ok {
		return ServiceKey{}, false
	}
	return matchService(host, proven, workloadNamespace, exists)
}

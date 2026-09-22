package depgraph

import "testing"

func TestExtractHostCandidate(t *testing.T) {
	cases := []struct {
		name       string
		value      string
		wantHost   string
		wantProven bool
		wantOK     bool
	}{
		{"url", "http://redis:6379/0", "redis", true, true},
		{"url with scheme https and path", "https://web.default.svc.cluster.local/healthz", "web.default.svc.cluster.local", true, true},
		{"host port", "redis:6379", "redis", true, true},
		{"qualified two label bare", "web.default", "web.default", true, true},
		{"qualified three label bare", "web.default.svc", "web.default.svc", true, true},
		{"bare single label", "web", "web", false, true},
		{"mode word", "debug", "debug", false, true},
		{"not hostlike, has space", "hello world", "", false, false},
		{"empty", "", "", false, false},
		{"number", "6379", "6379", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, proven, ok := extractHostCandidate(tc.value)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if host != tc.wantHost {
				t.Errorf("host = %q, want %q", host, tc.wantHost)
			}
			if proven != tc.wantProven {
				t.Errorf("proven = %v, want %v", proven, tc.wantProven)
			}
		})
	}
}

func TestMatchServiceRef(t *testing.T) {
	existing := map[ServiceKey]bool{
		{Namespace: "default", Name: "web"}:     true,
		{Namespace: "default", Name: "redis"}:   true,
		{Namespace: "other-ns", Name: "web"}:    true,
		{Namespace: "default", Name: "session"}: true,
	}
	exists := func(ns, name string) bool { return existing[ServiceKey{Namespace: ns, Name: name}] }

	cases := []struct {
		name      string
		value     string
		workload  string // workload's namespace
		wantMatch bool
		wantKey   ServiceKey
	}{
		{
			name:      "bare word never creates an edge",
			value:     "web",
			workload:  "default",
			wantMatch: false,
		},
		{
			name:      "url host in same namespace matches",
			value:     "http://web:8080",
			workload:  "default",
			wantMatch: true,
			wantKey:   ServiceKey{Namespace: "default", Name: "web"},
		},
		{
			name:      "host:port in same namespace matches",
			value:     "redis:6379",
			workload:  "default",
			wantMatch: true,
			wantKey:   ServiceKey{Namespace: "default", Name: "redis"},
		},
		{
			name:      "bare short name in a DIFFERENT namespace does not match even in host position",
			value:     "http://web:8080",
			workload:  "some-other-ns",
			wantMatch: false,
		},
		{
			name:      "qualified svc.ns matches for any namespace",
			value:     "web.other-ns",
			workload:  "default",
			wantMatch: true,
			wantKey:   ServiceKey{Namespace: "other-ns", Name: "web"},
		},
		{
			name:      "qualified svc.ns.svc matches",
			value:     "session.default.svc",
			workload:  "some-ns",
			wantMatch: true,
			wantKey:   ServiceKey{Namespace: "default", Name: "session"},
		},
		{
			name:      "qualified svc.ns.svc.<cluster-domain> matches regardless of the domain suffix",
			value:     "session.default.svc.cluster.local",
			workload:  "some-ns",
			wantMatch: true,
			wantKey:   ServiceKey{Namespace: "default", Name: "session"},
		},
		{
			name:      "qualified name pointing at a service that does not exist does not match",
			value:     "ghost.default.svc.cluster.local",
			workload:  "some-ns",
			wantMatch: false,
		},
		{
			name:      "bare word for a service that does not exist does not match",
			value:     "nonexistent",
			workload:  "default",
			wantMatch: false,
		},
		{
			name:      "unrelated numeric value does not match",
			value:     "6379",
			workload:  "default",
			wantMatch: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := matchServiceRef(tc.value, tc.workload, exists)
			if ok != tc.wantMatch {
				t.Fatalf("matched = %v, want %v (key=%v)", ok, tc.wantMatch, key)
			}
			if ok && key != tc.wantKey {
				t.Errorf("key = %v, want %v", key, tc.wantKey)
			}
		})
	}
}

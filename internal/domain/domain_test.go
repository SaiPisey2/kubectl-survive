// internal/domain/domain_test.go
package domain

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func node(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func TestOf(t *testing.T) {
	tests := []struct {
		name   string
		node   *corev1.Node
		key    string
		want   string
		wantOK bool
	}{
		{"stable label", node("n1", map[string]string{LabelZone: "us-east-1a"}), LabelZone, "us-east-1a", true},
		{"falls back to deprecated", node("n2", map[string]string{LabelZoneDeprecated: "us-east-1b"}), LabelZone, "us-east-1b", true},
		{"stable wins over deprecated", node("n3", map[string]string{LabelZone: "a", LabelZoneDeprecated: "b"}), LabelZone, "a", true},
		{"no fallback for custom keys", node("n4", map[string]string{LabelZoneDeprecated: "b"}), "rack", "", false},
		{"unlabelled node", node("n5", nil), LabelZone, "", false},
		{"empty value is not a domain", node("n6", map[string]string{LabelZone: ""}), LabelZone, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Of(tt.node, tt.key)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("Of() = (%q,%v), want (%q,%v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestGroupReportsUnlabelledNodes(t *testing.T) {
	nodes := []*corev1.Node{
		node("a1", map[string]string{LabelZone: "a"}),
		node("a2", map[string]string{LabelZone: "a"}),
		node("b1", map[string]string{LabelZone: "b"}),
		node("orphan", nil),
	}
	groups, unlabelled := Group(nodes, LabelZone)
	if len(groups["a"]) != 2 || len(groups["b"]) != 1 {
		t.Errorf("groups = %v, want a:2 b:1", groups)
	}
	if len(unlabelled) != 1 || unlabelled[0] != "orphan" {
		t.Errorf("unlabelled = %v, want [orphan]", unlabelled)
	}
}

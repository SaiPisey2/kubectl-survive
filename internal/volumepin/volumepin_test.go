// internal/volumepin/volumepin_test.go
package volumepin

import (
	"testing"

	"github.com/SaiPisey2/kubectl-survive/internal/domain"
	"github.com/SaiPisey2/kubectl-survive/internal/snapshot"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func zonalPV(name, zoneKey, zone string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{
			NodeAffinity: &corev1.VolumeNodeAffinity{
				Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key: zoneKey, Operator: corev1.NodeSelectorOpIn, Values: []string{zone},
					}},
				}}},
			},
		},
	}
}

func podWithPVC(claim string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "db-0", Namespace: "default"},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
			},
		}}},
	}
}

func snapWith(pv *corev1.PersistentVolume) *snapshot.Snapshot {
	return &snapshot.Snapshot{
		PVCs: []*corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default"},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: pv.Name},
		}},
		PVs: []*corev1.PersistentVolume{pv},
	}
}

func TestForPodDetectsStableZoneLabel(t *testing.T) {
	pins := ForPod(snapWith(zonalPV("pv-1", domain.LabelZone, "us-east-1a")), podWithPVC("data"), domain.LabelZone)
	if len(pins) != 1 || pins[0].Domain != "us-east-1a" {
		t.Fatalf("pins = %+v, want one pin to us-east-1a", pins)
	}
}

// PVs provisioned years ago may carry only the deprecated label.
func TestForPodDetectsDeprecatedZoneLabel(t *testing.T) {
	pins := ForPod(snapWith(zonalPV("pv-2", domain.LabelZoneDeprecated, "us-east-1b")), podWithPVC("data"), domain.LabelZone)
	if len(pins) != 1 || pins[0].Domain != "us-east-1b" {
		t.Fatalf("pins = %+v, want one pin to us-east-1b", pins)
	}
}

func TestForPodIgnoresUnpinnedVolumes(t *testing.T) {
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-3"}}
	if pins := ForPod(snapWith(pv), podWithPVC("data"), domain.LabelZone); len(pins) != 0 {
		t.Errorf("pins = %+v, want none", pins)
	}
}

func multiTermPV(name string, zones ...string) *corev1.PersistentVolume {
	var terms []corev1.NodeSelectorTerm
	for _, z := range zones {
		terms = append(terms, corev1.NodeSelectorTerm{
			MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key: domain.LabelZone, Operator: corev1.NodeSelectorOpIn, Values: []string{z},
			}},
		})
	}
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{
			NodeAffinity: &corev1.VolumeNodeAffinity{
				Required: &corev1.NodeSelector{NodeSelectorTerms: terms},
			},
		},
	}
}

// Terms are ORed: reachable from either zone means not pinned to one.
func TestForPodIgnoresVolumeReachableFromTwoZones(t *testing.T) {
	pv := multiTermPV("pv-multi", "us-east-1a", "us-east-1b")
	if pins := ForPod(snapWith(pv), podWithPVC("data"), domain.LabelZone); len(pins) != 0 {
		t.Errorf("pins = %+v, want none: the volume is reachable from two zones", pins)
	}
}

// Two terms naming the SAME zone still pin the volume to that zone.
func TestForPodDetectsPinWhenEveryTermNamesTheSameZone(t *testing.T) {
	pv := multiTermPV("pv-same", "us-east-1a", "us-east-1a")
	pins := ForPod(snapWith(pv), podWithPVC("data"), domain.LabelZone)
	if len(pins) != 1 || pins[0].Domain != "us-east-1a" {
		t.Fatalf("pins = %+v, want one pin to us-east-1a", pins)
	}
}

// A term with no zone constraint permits any zone, so the volume is not pinned.
func TestForPodIgnoresVolumeWithAnUnconstrainedTerm(t *testing.T) {
	pv := multiTermPV("pv-open", "us-east-1a")
	pv.Spec.NodeAffinity.Required.NodeSelectorTerms = append(
		pv.Spec.NodeAffinity.Required.NodeSelectorTerms,
		corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{
			Key: "kubernetes.io/os", Operator: corev1.NodeSelectorOpIn, Values: []string{"linux"},
		}}},
	)
	if pins := ForPod(snapWith(pv), podWithPVC("data"), domain.LabelZone); len(pins) != 0 {
		t.Errorf("pins = %+v, want none: one term permits any zone", pins)
	}
}

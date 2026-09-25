package volume

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/SaiPisey2/sounding/pkg/cascade"
	"github.com/SaiPisey2/sounding/pkg/cluster"
	"github.com/SaiPisey2/sounding/pkg/model"
)

// The core claim of the project, as a single test that flips on one field.
func TestReclaimPolicyDecidesWhetherDataSurvives(t *testing.T) {
	for _, tc := range []struct {
		policy    corev1.PersistentVolumeReclaimPolicy
		wantClass model.Class
		wantKind  string
	}{
		{corev1.PersistentVolumeReclaimRetain, model.ClassCompensable, "detaches-data"},
		{corev1.PersistentVolumeReclaimDelete, model.ClassTerminal, "destroys-data"},
	} {
		t.Run(string(tc.policy), func(t *testing.T) {
			eff, class := classifyPV("pg-data", "pv-1", tc.policy)
			if class != tc.wantClass {
				t.Errorf("class = %v, want %v", class, tc.wantClass)
			}
			if eff.Kind != tc.wantKind {
				t.Errorf("effect kind = %q, want %q", eff.Kind, tc.wantKind)
			}
			if eff.Basis != model.BasisComputed {
				t.Errorf("basis = %v, want computed -- this was read from the PV", eff.Basis)
			}
		})
	}
}

// Recycle is deprecated and removed in modern Kubernetes. If a cluster still
// reports it, say so rather than mapping it onto one of the other two.
func TestRecycleIsReportedRatherThanGuessed(t *testing.T) {
	eff, class := classifyPV("old", "pv-2", corev1.PersistentVolumeReclaimRecycle)
	if class != model.ClassTerminal {
		t.Errorf("class = %v, want TERMINAL for an unrecognised policy", class)
	}
	if eff.Basis != model.BasisUnknown {
		t.Errorf("basis = %v, want unknown", eff.Basis)
	}
}

// An unbound PVC has no volumeName. It must not be silently skipped, and it
// must not be reported as safe -- nothing is known about it.
func TestUnboundClaimIsReportedAsUnknown(t *testing.T) {
	eff, class := classifyUnbound("pending-claim")
	if class != model.ClassTerminal {
		t.Errorf("class = %v, want TERMINAL", class)
	}
	if eff.Basis != model.BasisUnknown {
		t.Errorf("basis = %v, want unknown", eff.Basis)
	}
}

// Join selected PVCs by Target.Resource alone, so a same-named-but-foreign
// resource -- a CRD or aggregated API registering its own "resource" named
// "persistentvolumeclaims" under a different group -- would be fed into the
// core/v1-specific Get calls below and fail the whole join, or worse, be
// silently matched against an unrelated real PVC of the same name. Group
// must be checked too: only the core group ("") is ever a real
// PersistentVolumeClaim.
func TestJoinIgnoresAForeignGroupsPersistentVolumeClaims(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "ns"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pv-1"},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-1"},
		Spec:       corev1.PersistentVolumeSpec{PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain},
	}
	c := &cluster.Clients{Typed: fake.NewSimpleClientset(pvc, pv), Calls: new(int64)}

	objs := []cascade.Object{
		{Target: model.Target{Resource: "persistentvolumeclaims", Group: "", Namespace: "ns", Name: "data"}},
		// Same Resource string, foreign group. Nothing in the fake
		// clientset backs this name -- if Join ever tried to Get it as a
		// real core/v1 PVC, this test would fail with an error, not
		// silently pass.
		{Target: model.Target{Resource: "persistentvolumeclaims", Group: "widgets.example.com", Namespace: "ns", Name: "not-a-real-pvc"}},
	}

	effects, class, err := Join(context.Background(), c, objs)
	if err != nil {
		t.Fatalf("Join errored: %v", err)
	}
	if len(effects) != 1 {
		t.Fatalf("got %d effects, want 1 -- the foreign-group object must be ignored: %+v", len(effects), effects)
	}
	if class != model.ClassCompensable {
		t.Errorf("class = %v, want ClassCompensable", class)
	}
}

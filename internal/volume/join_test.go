package volume

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/SaiPisey2/sounding/internal/model"
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

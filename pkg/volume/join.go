// Package volume closes the one join a namespace walk cannot make on its
// own: PersistentVolumes are cluster-scoped, so enumerating a namespace only
// ever surfaces the PVC. Whether the data underneath survives a mutation is
// decided by the bound PV's reclaim policy, reached through spec.volumeName
// rather than an ownerReference, so no ownership walk finds it either.
package volume

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/SaiPisey2/sounding/pkg/cascade"
	"github.com/SaiPisey2/sounding/pkg/cluster"
	"github.com/SaiPisey2/sounding/pkg/model"
)

func classifyPV(claim, pv string, p corev1.PersistentVolumeReclaimPolicy) (model.Effect, model.Class) {
	t := model.Target{Resource: "persistentvolumes", Kind: "PersistentVolume", Name: pv}
	switch p {
	case corev1.PersistentVolumeReclaimRetain:
		return model.Effect{
			Kind: "detaches-data", Object: t, Basis: model.BasisComputed,
			Explanation: fmt.Sprintf("pvc/%s is bound to pv/%s with reclaimPolicy=Retain: the volume and its data survive and the pv moves to Released", claim, pv),
		}, model.ClassCompensable
	case corev1.PersistentVolumeReclaimDelete:
		return model.Effect{
			Kind: "destroys-data", Object: t, Basis: model.BasisComputed,
			Explanation: fmt.Sprintf("pvc/%s is bound to pv/%s with reclaimPolicy=Delete: the csi driver destroys the underlying volume and the data is not recoverable", claim, pv),
		}, model.ClassTerminal
	default:
		// Recycle is deprecated and removed. Anything else is a policy we do
		// not understand, and guessing which of the two real outcomes it
		// resembles is exactly the kind of assumption this tool exists to
		// refuse.
		return model.Effect{
			Kind: "unknown-data-fate", Object: t, Basis: model.BasisUnknown,
			Explanation: fmt.Sprintf("pvc/%s is bound to pv/%s with reclaimPolicy=%q, which this version does not recognise", claim, pv, p),
		}, model.ClassTerminal
	}
}

func classifyUnbound(claim string) (model.Effect, model.Class) {
	return model.Effect{
		Kind: "unknown-data-fate", Basis: model.BasisUnknown,
		Object:      model.Target{Resource: "persistentvolumeclaims", Kind: "PersistentVolumeClaim", Name: claim},
		Explanation: fmt.Sprintf("pvc/%s has no spec.volumeName, so nothing is known about what backs it", claim),
	}, model.ClassTerminal
}

// Join finds every PVC among objs, follows spec.volumeName to the
// cluster-scoped PV, and returns one effect per PVC describing whether its
// data survives.
func Join(ctx context.Context, c *cluster.Clients, objs []cascade.Object) ([]model.Effect, model.Class, error) {
	var effects []model.Effect
	worst := model.ClassRead
	for _, o := range objs {
		// Group must be checked alongside Resource: "persistentvolumeclaims"
		// is the core group's own name for this kind, but nothing stops a
		// CRD or aggregated API from registering a resource with the same
		// plural under its own group. Matching on Resource alone would feed
		// a same-named-but-foreign object into the PVC-specific Get calls
		// below, which expect the real core/v1 PersistentVolumeClaim shape.
		if o.Target.Resource != "persistentvolumeclaims" || o.Target.Group != "" {
			continue
		}
		claim, err := c.Typed.CoreV1().PersistentVolumeClaims(o.Target.Namespace).Get(ctx, o.Target.Name, metav1.GetOptions{})
		*c.Calls++
		if err != nil {
			return nil, worst, fmt.Errorf("reading pvc/%s: %w", o.Target.Name, err)
		}
		if claim.Spec.VolumeName == "" {
			e, cl := classifyUnbound(o.Target.Name)
			effects, worst = append(effects, e), max(worst, cl)
			continue
		}
		pv, err := c.Typed.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		*c.Calls++
		if err != nil {
			return nil, worst, fmt.Errorf("reading pv/%s bound to pvc/%s: %w", claim.Spec.VolumeName, o.Target.Name, err)
		}
		e, cl := classifyPV(o.Target.Name, pv.Name, pv.Spec.PersistentVolumeReclaimPolicy)
		effects, worst = append(effects, e), max(worst, cl)
	}
	return effects, worst, nil
}

func max(a, b model.Class) model.Class {
	if b > a {
		return b
	}
	return a
}

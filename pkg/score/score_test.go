package score

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/SaiPisey2/sounding/internal/report"
	"github.com/SaiPisey2/sounding/pkg/cascade"
	"github.com/SaiPisey2/sounding/pkg/cluster"
	"github.com/SaiPisey2/sounding/pkg/model"
)

// model.Classify only ever raises a finding to the floor its weakest
// EVIDENCE imposes; it knows nothing about what the verb being scored
// does. Every effect enumeration produces here carries BasisComputed, whose
// own floor is ClassRead -- the most permissive class there is -- so this
// pins that classify() supplies its own floor on top of that rather than
// trusting Classify's basis rules alone. Dropping verbFloor() in favour of
// model.ClassRead is exactly the regression this exists to catch: it
// compiles, and it would print READ for a namespace full of measured,
// destructive effects.
func TestClassifyAppliesTheVerbFloorEvenWhenEveryEffectIsComputed(t *testing.T) {
	act := model.Action{Verb: "delete", Target: model.Target{Resource: "namespaces", Name: "prod-payments"}}
	effects := []model.Effect{
		{Kind: "destroys", Basis: model.BasisComputed, Object: model.Target{Kind: "Pod", Name: "api-1"}, Explanation: "in the namespace"},
	}

	got, err := classify(act, effects, model.ClassRead)
	if err != nil {
		t.Fatalf("classify returned an error: %v", err)
	}
	if got.Class == model.ClassRead {
		t.Fatalf("class = %v, want at least the verb floor -- a namespace deletion must never classify as READ", got.Class)
	}
	if got.Class != model.ClassCompensable {
		t.Errorf("class = %v, want ClassCompensable (the delete-namespace verb floor)", got.Class)
	}
}

// A namespace whose volumes are worse than the verb floor must win: the
// verb floor is a MINIMUM the volume join can raise, never a ceiling.
func TestClassifyLetsVolumeClassRaiseAboveTheVerbFloor(t *testing.T) {
	act := model.Action{Verb: "delete", Target: model.Target{Resource: "namespaces", Name: "prod-payments"}}
	got, err := classify(act, nil, model.ClassTerminal)
	if err != nil {
		t.Fatalf("classify returned an error: %v", err)
	}
	if got.Class != model.ClassTerminal {
		t.Errorf("class = %v, want ClassTerminal to survive over the ClassCompensable verb floor", got.Class)
	}
}

// verbFloor requires an explicit case per verb rather than a constant every
// caller reuses; a verb it does not recognise must refuse rather than
// silently inherit "delete"'s floor.
func TestClassifyRefusesAVerbWithNoFloorDefined(t *testing.T) {
	act := model.Action{Verb: "scale", Target: model.Target{Resource: "deployments", Name: "api"}}
	_, err := classify(act, nil, model.ClassRead)
	if err == nil {
		t.Fatal("want an error for a verb with no floor defined, got nil")
	}
	if !strings.Contains(err.Error(), "scale") {
		t.Errorf("error must name the verb: %v", err)
	}
}

// This is the exact bug a live-cluster run surfaced: cascade.Enumerate's
// own output order is whatever cluster.ListableNamespaced's resource list
// happens to be in -- sorted lexicographically by GroupVersionResource
// string, so the core group ("") sorts ahead of "apps" -- not cascade
// order. cascade.Order itself is exhaustively tested and every one of
// those tests keeps passing whether or not anything actually calls it; the
// only way to catch a dropped call is to check what the command renders.
// The fixture below is deliberately built out of cascade order -- child
// before parent before grandparent -- because that is the shape Enumerate
// actually produces against a real cluster, not an arbitrary shuffle.
func TestRenderedReportListsOwnerBeforeOwned(t *testing.T) {
	dep := types.UID("dep-1")
	rs := types.UID("rs-1")
	pod := types.UID("pod-1")
	objs := []cascade.Object{
		{Target: model.Target{Kind: "Pod", Name: "web-1"}, UID: pod, Owners: []types.UID{rs}},
		{Target: model.Target{Kind: "ReplicaSet", Name: "web"}, UID: rs, Owners: []types.UID{dep}},
		{Target: model.Target{Kind: "Deployment", Name: "web"}, UID: dep},
	}

	effects := destroyEffectsFromObjects(objs)
	act := model.Action{Verb: "delete", Target: model.Target{Resource: "namespaces", Name: "prod"}}
	finding, err := classify(act, effects, model.ClassRead)
	if err != nil {
		t.Fatalf("classify errored: %v", err)
	}

	var b bytes.Buffer
	report.Write(&b, finding)
	s := b.String()

	depIdx := strings.Index(s, "Deployment/web")
	rsIdx := strings.Index(s, "ReplicaSet/web")
	podIdx := strings.Index(s, "Pod/web-1")
	if depIdx < 0 || rsIdx < 0 || podIdx < 0 {
		t.Fatalf("rendered report is missing an expected object:\n%s", s)
	}
	if !(depIdx < rsIdx && rsIdx < podIdx) {
		t.Errorf("want Deployment before ReplicaSet before Pod in the rendered report, got:\n%s", s)
	}
}

func TestTargetNamespaceRecognisesNamespaceDeleteDirectly(t *testing.T) {
	act := model.Action{Verb: "delete", Target: model.Target{Resource: "namespaces", Name: "prod-payments"}}
	ns, err := targetNamespace(act, nil)
	if err != nil {
		t.Fatalf("targetNamespace returned an error: %v", err)
	}
	if ns != "prod-payments" {
		t.Errorf("ns = %q, want prod-payments", ns)
	}
}

func TestTargetNamespaceRefusesAVerbWithNoAnalyser(t *testing.T) {
	act := model.Action{Verb: "scale", Target: model.Target{Resource: "deployments", Name: "api"}}
	_, err := targetNamespace(act, nil)
	if err == nil {
		t.Fatal("want a refusal for an unanalysed verb, got nil")
	}
	if !strings.Contains(err.Error(), "scale") {
		t.Errorf("refusal must name the verb it could not analyse: %v", err)
	}
}

// Namespace is cluster-scoped and therefore never appears in
// ListableNamespaced's output; a resource that legitimately exists but
// this build has no analyzer for must still be resolved against discovery
// before being named in the refusal, rather than repeating whatever the
// caller typed. The input here is deliberately the SINGULAR "pod", not the
// plural "pods": asserting the refusal names "pods" only proves something
// if the input could not already satisfy that assertion on its own --
// with "pods" in and "pods" asserted, the test would pass even if
// targetNamespace echoed the unresolved string straight back without
// calling cluster.ResolveResource at all.
func TestTargetNamespaceResolvesOtherResourcesBeforeRefusing(t *testing.T) {
	rs := []cluster.Resource{
		{GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Kind: "Pod", Namespaced: true, SingularName: "pod"},
	}
	act := model.Action{Verb: "delete", Target: model.Target{Resource: "pod", Name: "api-1", Namespace: "prod"}}
	_, err := targetNamespace(act, rs)
	if err == nil {
		t.Fatal("want a refusal -- only delete namespace is supported, got nil")
	}
	if !strings.Contains(err.Error(), "pods") {
		t.Errorf("refusal must name the resolved resource: %v", err)
	}
}

func TestTargetNamespacePropagatesAnAmbiguousResourceRefusal(t *testing.T) {
	rs := []cluster.Resource{
		{GVR: schema.GroupVersionResource{Version: "v1", Resource: "ingresses"}, Kind: "Ingress", Namespaced: true, SingularName: "ingress"},
		{GVR: schema.GroupVersionResource{Group: "widgets.example.com", Version: "v1", Resource: "ingressclasses"}, Kind: "IngressClass", Namespaced: true, SingularName: "ingress"},
	}
	act := model.Action{Verb: "delete", Target: model.Target{Resource: "ingress", Name: "x", Namespace: "prod"}}
	_, err := targetNamespace(act, rs)
	if err == nil {
		t.Fatal("want the ambiguous-match refusal to propagate, got nil")
	}
	if !strings.Contains(err.Error(), "ingresses") || !strings.Contains(err.Error(), "ingressclasses") {
		t.Errorf("refusal must name both candidates: %v", err)
	}
}

// namespaceMustExist is the guard that separates "this namespace does not
// exist" from "this namespace exists and is genuinely empty" -- the two
// states cascade.Enumerate alone cannot tell apart, since both list zero
// objects across zero kinds. A fake clientset stands in for the live
// cluster here because namespaceMustExist's own contract is entirely about
// what it does with a Get's result, not about discovery or enumeration.
func TestNamespaceMustExistRefusesOnNotFound(t *testing.T) {
	c := &cluster.Clients{Typed: fake.NewSimpleClientset(), Calls: new(int64)}
	err := namespaceMustExist(context.Background(), c, "definitely-not-a-real-namespace-xyz")
	if err == nil {
		t.Fatal("want a refusal for a namespace that does not exist, got nil")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("error %v does not wrap ErrRefused", err)
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-namespace-xyz") {
		t.Errorf("refusal must name the missing namespace: %v", err)
	}
}

func TestNamespaceMustExistPassesWhenTheNamespaceIsPresent(t *testing.T) {
	c := &cluster.Clients{
		Typed: fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "sounding-demo"},
		}),
		Calls: new(int64),
	}
	if err := namespaceMustExist(context.Background(), c, "sounding-demo"); err != nil {
		t.Errorf("want no error for a namespace that exists, got %v", err)
	}
}

// excludedFromEffects feeds NOT-RESTORED.txt, and must name every effect
// whose OBJECT is genuinely absent from the bundle -- not just the effects
// whose DATA is destroyed. Before this fix, detaches-data (a Retain
// PersistentVolume) fell through this function entirely: its object is
// cluster-scoped and was never fetched into the bundle either, but nothing
// said so. A report with one "destroys" effect, one "destroys-data" effect
// and one "detaches-data" effect -- both PV-shaped effects carrying
// Resource: "persistentvolumes", exactly as classifyPV builds them -- must
// list 2 exclusions, not 1.
func TestExcludedFromEffectsNamesTheRetainVolumeToo(t *testing.T) {
	effects := []model.Effect{
		{Kind: "destroys", Object: model.Target{Kind: "Pod", Name: "web-1"}, Explanation: "in the namespace"},
		{Kind: "destroys-data", Object: model.Target{Resource: "persistentvolumes", Kind: "PersistentVolume", Name: "pv-delete"}, Explanation: "reclaimPolicy=Delete"},
		{Kind: "detaches-data", Object: model.Target{Resource: "persistentvolumes", Kind: "PersistentVolume", Name: "pv-retain"}, Explanation: "reclaimPolicy=Retain"},
	}

	excluded := excludedFromEffects(effects)
	if len(excluded) != 2 {
		t.Fatalf("excludedFromEffects returned %d entries, want 2 (destroys-data + detaches-data):\n%v", len(excluded), excluded)
	}

	joined := strings.Join(excluded, "\n")
	if !strings.Contains(joined, "pv-delete") {
		t.Errorf("must name the destroyed volume: %v", excluded)
	}
	if !strings.Contains(joined, "pv-retain") {
		t.Errorf("must name the retained-but-uncaptured volume: %v", excluded)
	}
	if !strings.Contains(joined, "claimRef") {
		t.Errorf("the retained-volume entry must explain the claimRef needs clearing before the restored pvc can rebind: %v", excluded)
	}
}

// An unbound PVC's unknown-data-fate effect (pkg/volume/join.go's
// classifyUnbound) names the CLAIM itself, not a volume -- there is no PV
// to name. The claim is namespaced, so cascade.Enumerate already walked it
// into objs and snapshot.Write already wrote its manifest to the bundle
// exactly like every other object in the namespace. Before this fix,
// excludedFromEffects put it in NOT-RESTORED.txt anyway, wholesale, because
// it matched on effect Kind alone -- so the file claimed an object was not
// captured when a JSON manifest for it sat in the very same directory and
// restore.sh applied it. An operator following the file's own instructions
// would have hand-recreated a PVC the bundle already restores.
func TestExcludedFromEffectsDoesNotClaimAnAlreadyCapturedPVCIsMissing(t *testing.T) {
	effects := []model.Effect{
		{Kind: "destroys", Object: model.Target{Resource: "persistentvolumeclaims", Kind: "PersistentVolumeClaim", Name: "orphan"}, Explanation: "in the namespace"},
		{Kind: "unknown-data-fate", Object: model.Target{Resource: "persistentvolumeclaims", Kind: "PersistentVolumeClaim", Name: "orphan"}, Explanation: "pvc/orphan has no spec.volumeName, so nothing is known about what backs it"},
	}

	excluded := excludedFromEffects(effects)
	if len(excluded) != 0 {
		t.Fatalf("excludedFromEffects returned %v, want none -- the claim's own manifest is already in the bundle", excluded)
	}
}

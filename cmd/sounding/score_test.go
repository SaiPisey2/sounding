package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/SaiPisey2/sounding/internal/cascade"
	"github.com/SaiPisey2/sounding/internal/cluster"
	"github.com/SaiPisey2/sounding/internal/model"
	"github.com/SaiPisey2/sounding/internal/report"
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

// Encoding model.Finding directly would print Class as a bare int -- 3 for
// TERMINAL, which happens to be COMPENSABLE's own exit code. This is the
// contract test for --json: it must produce valid JSON, and the class must
// read as its name, with the effects and their bases present so a
// machine reader has the same evidence a human reading the plain report
// does.
func TestJSONOutputRendersTheClassAsItsNameNotACollidingNumber(t *testing.T) {
	f := model.Finding{
		Action: model.Action{Verb: "delete", Target: model.Target{Resource: "namespaces", Name: "prod-payments"}},
		Effects: []model.Effect{
			{Kind: "destroys", Basis: model.BasisComputed, Object: model.Target{Kind: "Pod", Name: "api-1"}, Explanation: "in the namespace"},
			{Kind: "destroys-data", Basis: model.BasisComputed, Object: model.Target{Kind: "PersistentVolume", Name: "pv-1"}, Explanation: "reclaimPolicy=Delete"},
		},
		Class: model.ClassTerminal, Scanned: time.Unix(0, 0).UTC(), APICalls: 61,
	}

	var b bytes.Buffer
	if err := writeJSON(&b, f); err != nil {
		t.Fatalf("writeJSON errored: %v", err)
	}

	var decoded struct {
		Class   string `json:"class"`
		Effects []struct {
			Basis string `json:"Basis"`
		} `json:"effects"`
	}
	if err := json.Unmarshal(b.Bytes(), &decoded); err != nil {
		t.Fatalf("--json output does not parse as JSON: %v\n%s", err, b.String())
	}
	if decoded.Class != "TERMINAL" {
		t.Errorf("class = %q, want \"TERMINAL\" (a bare 3 collides with COMPENSABLE's exit code)", decoded.Class)
	}
	if len(decoded.Effects) != 2 {
		t.Fatalf("got %d effects in the json output, want 2", len(decoded.Effects))
	}
	for _, e := range decoded.Effects {
		if e.Basis != "computed" {
			t.Errorf("effect basis = %q, want \"computed\"", e.Basis)
		}
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
	if !errors.Is(err, errRefused) {
		t.Errorf("error %v does not wrap errRefused", err)
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-namespace-xyz") {
		t.Errorf("refusal must name the missing namespace: %v", err)
	}
	if exitCodeForError(err) != 2 {
		t.Errorf("exit code = %d, want 2", exitCodeForError(err))
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

package main

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/SaiPisey2/sounding/internal/cluster"
	"github.com/SaiPisey2/sounding/internal/model"
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

	got := classify(act, effects, model.ClassRead)
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
	got := classify(act, nil, model.ClassTerminal)
	if got.Class != model.ClassTerminal {
		t.Errorf("class = %v, want ClassTerminal to survive over the ClassCompensable verb floor", got.Class)
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
// before being named in the refusal, rather than repeating whatever plural
// action.ParseCommand guessed.
func TestTargetNamespaceResolvesOtherResourcesBeforeRefusing(t *testing.T) {
	rs := []cluster.Resource{
		{GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Kind: "Pod", Namespaced: true, SingularName: "pod"},
	}
	act := model.Action{Verb: "delete", Target: model.Target{Resource: "pods", Name: "api-1", Namespace: "prod"}}
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

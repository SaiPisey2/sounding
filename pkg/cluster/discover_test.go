package cluster

import (
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestSelectListableNamespaced(t *testing.T) {
	lists := []*metav1.APIResourceList{{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: []string{"list", "get"}},
			{Name: "pods/log", Kind: "Pod", Namespaced: true, Verbs: []string{"get"}},
			{Name: "bindings", Kind: "Binding", Namespaced: true, Verbs: []string{"create"}},
			{Name: "nodes", Kind: "Node", Namespaced: false, Verbs: []string{"list"}},
		},
	}}
	got, err := selectListable(lists)
	if err != nil {
		t.Fatalf("selectListable errored: %v", err)
	}
	if len(got) != 1 || got[0].GVR.Resource != "pods" {
		t.Fatalf("got %+v, want only pods", got)
	}
}

// A subresource has a slash in its name and listing it is meaningless; a
// resource without the list verb cannot be enumerated at all. Both must be
// skipped, or the enumeration errors on things that were never objects.
//
// pods/log deliberately carries the list verb: real clusters essentially
// never expose list on a subresource, so a fixture built only from realistic
// data (like pods/exec above, which lacks list) would leave the slash check
// unpinned -- hasVerb alone would already exclude every subresource here,
// and disabling the slash check would go unnoticed.
func TestSelectListableSkipsSubresourcesAndUnlistables(t *testing.T) {
	lists := []*metav1.APIResourceList{{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "pods/exec", Kind: "Pod", Namespaced: true, Verbs: []string{"create"}},
			{Name: "pods/log", Kind: "Pod", Namespaced: true, Verbs: []string{"list"}},
			{Name: "events", Kind: "Event", Namespaced: true, Verbs: []string{"list"}},
		},
	}}
	got, _ := selectListable(lists)
	for _, r := range got {
		if strings.Contains(r.GVR.Resource, "/") {
			t.Errorf("subresource %q was not skipped", r.GVR.Resource)
		}
	}
	if len(got) != 1 {
		t.Errorf("got %d resources, want 1", len(got))
	}
}

func TestSelectListableRejectsAnUnparseableGroupVersion(t *testing.T) {
	lists := []*metav1.APIResourceList{{GroupVersion: "a/b/c", APIResources: []metav1.APIResource{
		{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"list"}},
	}}}
	if _, err := selectListable(lists); err == nil {
		t.Fatal("want an error for an unparseable groupVersion")
	}
}

// Discovery returns partial results ALONGSIDE an error when an api group is
// unreachable. Treating that as success produces a shorter resource list, a
// smaller enumeration, and a blast radius that reads as safe precisely
// because something was broken.
func TestPartialDiscoveryIsFatalAndNamesTheGroups(t *testing.T) {
	partial := &discoveryFailure{groups: []string{"metrics.k8s.io/v1beta1"}}
	err := wrapDiscoveryError(partial)
	if err == nil {
		t.Fatal("a partial discovery result must not be treated as success")
	}
	if !errors.Is(err, ErrIncompleteDiscovery) {
		t.Errorf("error %v does not wrap ErrIncompleteDiscovery", err)
	}
	if !strings.Contains(err.Error(), "metrics.k8s.io/v1beta1") {
		t.Errorf("refusal must name the failed group, got %q", err.Error())
	}
}

// A bare connectivity failure -- no server to ask at all, as opposed to
// some groups answering and others not -- must come back unwrapped. Folding
// it into ErrIncompleteDiscovery would make the command exit 2 (a refusal:
// "your command is wrong") for a cluster that is simply down, when the
// right answer is an operational failure (exit 1: "sounding could not run
// right now").
func TestUnreachableClusterIsNotTreatedAsIncompleteDiscovery(t *testing.T) {
	unreachable := errors.New("dial tcp 10.0.0.1:6443: connect: connection refused")
	err := wrapDiscoveryError(unreachable)
	if err == nil {
		t.Fatal("want the error to propagate")
	}
	if errors.Is(err, ErrIncompleteDiscovery) {
		t.Errorf("a bare connectivity failure must not wrap ErrIncompleteDiscovery: %v", err)
	}
	if !errors.Is(err, unreachable) {
		t.Errorf("the original error must still be reachable via errors.Is: %v", err)
	}
}

// resourceSet builds a small fixture for ResolveResource tests: a stable
// namespaced resource plus a same-plural resource in a second group, which is
// the case that must fail rather than guess.
func resourceSet() []Resource {
	return []Resource{
		{
			GVR:          schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
			Kind:         "Pod",
			Namespaced:   true,
			SingularName: "pod",
			ShortNames:   []string{"po"},
		},
		{
			GVR:          schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			Kind:         "Deployment",
			Namespaced:   true,
			SingularName: "deployment",
			ShortNames:   []string{"deploy"},
		},
		{
			GVR:          schema.GroupVersionResource{Group: "acme.io", Version: "v1", Resource: "widgets"},
			Kind:         "Widget",
			Namespaced:   true,
			SingularName: "widget",
			ShortNames:   []string{"wg"},
		},
		{
			GVR:          schema.GroupVersionResource{Group: "other.io", Version: "v1", Resource: "widgets"},
			Kind:         "Widget",
			Namespaced:   true,
			SingularName: "widget",
			ShortNames:   []string{"wg"},
		},
	}
}

func TestResolveResourceMatchesExactPlural(t *testing.T) {
	got, err := ResolveResource(resourceSet(), "deployments")
	if err != nil {
		t.Fatalf("ResolveResource errored: %v", err)
	}
	if got.GVR.Resource != "deployments" {
		t.Fatalf("got %+v, want deployments", got)
	}
}

func TestResolveResourceMatchesSingular(t *testing.T) {
	got, err := ResolveResource(resourceSet(), "pod")
	if err != nil {
		t.Fatalf("ResolveResource errored: %v", err)
	}
	if got.GVR.Resource != "pods" {
		t.Fatalf("got %+v, want pods", got)
	}
}

func TestResolveResourceMatchesShortName(t *testing.T) {
	got, err := ResolveResource(resourceSet(), "PO")
	if err != nil {
		t.Fatalf("ResolveResource errored: %v", err)
	}
	if got.GVR.Resource != "pods" {
		t.Fatalf("got %+v, want pods (case-insensitive short name)", got)
	}
}

func TestResolveResourceRefusesUnknownString(t *testing.T) {
	if _, err := ResolveResource(resourceSet(), "wombats"); err == nil {
		t.Fatal("want an error for a resource string that matches nothing")
	}
}

func TestResolveResourceRefusesAmbiguousMatchAcrossGroups(t *testing.T) {
	_, err := ResolveResource(resourceSet(), "widgets")
	if err == nil {
		t.Fatal("want an error when the string matches resources in two different groups")
	}
	if !strings.Contains(err.Error(), "acme.io") || !strings.Contains(err.Error(), "other.io") {
		t.Errorf("refusal must name both candidate groups, got %q", err.Error())
	}
}

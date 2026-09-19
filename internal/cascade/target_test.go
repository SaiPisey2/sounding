package cascade

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	metadatafake "k8s.io/client-go/metadata/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/SaiPisey2/sounding/internal/cluster"
)

// PartialObjectMetadata carries only ObjectMeta -- no group, version,
// resource or kind. Enumerate must supply those four from the
// cluster.Resource the list call was made for, or every field but Name and
// Namespace is silently zero. The next component matches on
// Target.Resource == "persistentvolumeclaims" to find data at risk; a zero
// Resource field makes that match fail quietly, so this test checks every
// field individually rather than trusting a single non-empty check.
func TestEnumeratePopulatesEveryTargetField(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	cm := &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "demo", UID: "u1"},
	}

	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatalf("AddMetaToScheme: %v", err)
	}
	client := metadatafake.NewSimpleMetadataClient(scheme, cm)
	var calls int64
	c := &cluster.Clients{Metadata: client, Calls: &calls}

	rs := []cluster.Resource{{GVR: gvr, Kind: "ConfigMap", Namespaced: true}}

	got, err := Enumerate(context.Background(), c, rs, "demo")
	if err != nil {
		t.Fatalf("Enumerate errored: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d objects, want 1", len(got))
	}

	tgt := got[0].Target
	switch {
	case tgt.Group != "":
		t.Errorf("Target.Group = %q, want empty core group", tgt.Group)
	case tgt.Version != "v1":
		t.Errorf("Target.Version = %q, want v1", tgt.Version)
	case tgt.Resource != "configmaps":
		t.Errorf("Target.Resource = %q, want configmaps", tgt.Resource)
	case tgt.Kind != "ConfigMap":
		t.Errorf("Target.Kind = %q, want ConfigMap", tgt.Kind)
	case tgt.Namespace != "demo":
		t.Errorf("Target.Namespace = %q, want demo", tgt.Namespace)
	case tgt.Name != "settings":
		t.Errorf("Target.Name = %q, want settings", tgt.Name)
	}

	// *c.Calls is what the report prints as the scan's cost; a regression
	// that moved the increment inside the per-item loop, or dropped it on
	// an error path, would make the tool state something false about what
	// it did, and nothing else here would catch it.
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (one List call for one resource)", calls)
	}
}

// A list error must abort the whole enumeration rather than return whatever
// was gathered so far -- a shorter result reads as a smaller blast radius,
// which is worse than refusing outright.
func TestEnumerateAbortsOnAnyListError(t *testing.T) {
	scheme := runtime.NewScheme()
	client := metadatafake.NewSimpleMetadataClient(scheme)
	wantErr := errors.New("list failed")
	client.PrependReactor("list", "widgets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, wantErr
	})
	var calls int64
	c := &cluster.Clients{Metadata: client, Calls: &calls}

	rs := []cluster.Resource{
		{GVR: schema.GroupVersionResource{Version: "v1", Resource: "widgets"}, Kind: "Widget", Namespaced: true},
	}
	if _, err := Enumerate(context.Background(), c, rs, "demo"); !errors.Is(err, wantErr) {
		t.Fatalf("Enumerate error = %v, want %v", err, wantErr)
	}
	// The counter must still reflect the one call that was made, even
	// though that call failed -- a dropped increment on the error path
	// would undercount exactly the request that mattered most.
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (the failed call still counts)", calls)
	}
}

// A conformant cluster serves Events under both "v1" and "events.k8s.io/v1"
// (every cluster since 1.19), and ListableNamespaced lists both because it
// has no way to know ahead of time that they name the same objects. Without
// deduplication by UID, Enumerate reports the same object once per group it
// happens to be served under -- this is the failure a live cluster surfaced:
// the object count came out nearly double on a namespace full of Events.
func TestEnumerateDeduplicatesSameObjectAcrossGroups(t *testing.T) {
	core := &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Event"},
		ObjectMeta: metav1.ObjectMeta{Name: "pod-scheduled", Namespace: "demo", UID: "shared-uid"},
	}
	eventsGroup := &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "events.k8s.io/v1", Kind: "Event"},
		ObjectMeta: metav1.ObjectMeta{Name: "pod-scheduled", Namespace: "demo", UID: "shared-uid"},
	}

	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatalf("AddMetaToScheme: %v", err)
	}
	client := metadatafake.NewSimpleMetadataClient(scheme, core, eventsGroup)
	var calls int64
	c := &cluster.Clients{Metadata: client, Calls: &calls}

	rs := []cluster.Resource{
		{GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "events"}, Kind: "Event", Namespaced: true},
		{GVR: schema.GroupVersionResource{Group: "events.k8s.io", Version: "v1", Resource: "events"}, Kind: "Event", Namespaced: true},
	}

	got, err := Enumerate(context.Background(), c, rs, "demo")
	if err != nil {
		t.Fatalf("Enumerate errored: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d objects, want 1 -- the same object was served under two groups", len(got))
	}

	// Both list calls really happened and each cost a request, even though
	// the second one's result was entirely a duplicate; the report's call
	// count must not silently drop the request that turned out redundant.
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (both groups were listed)", calls)
	}
}

// A second group is not always redundant -- it may legitimately hold
// objects the first group does not. Deduplicating by UID must not turn into
// deduplicating by resource or group, or a genuinely distinct object from a
// second group would silently vanish the same way the duplicate should.
func TestEnumerateKeepsDistinctObjectsFromASecondGroup(t *testing.T) {
	fromCore := &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Event"},
		ObjectMeta: metav1.ObjectMeta{Name: "core-event", Namespace: "demo", UID: "uid-a"},
	}
	fromEventsGroup := &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "events.k8s.io/v1", Kind: "Event"},
		ObjectMeta: metav1.ObjectMeta{Name: "extra-event", Namespace: "demo", UID: "uid-b"},
	}

	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatalf("AddMetaToScheme: %v", err)
	}
	client := metadatafake.NewSimpleMetadataClient(scheme, fromCore, fromEventsGroup)
	var calls int64
	c := &cluster.Clients{Metadata: client, Calls: &calls}

	rs := []cluster.Resource{
		{GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "events"}, Kind: "Event", Namespaced: true},
		{GVR: schema.GroupVersionResource{Group: "events.k8s.io", Version: "v1", Resource: "events"}, Kind: "Event", Namespaced: true},
	}

	got, err := Enumerate(context.Background(), c, rs, "demo")
	if err != nil {
		t.Fatalf("Enumerate errored: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d objects, want 2 -- distinct UIDs must both survive", len(got))
	}
	seen := map[types.UID]bool{}
	for _, o := range got {
		seen[o.UID] = true
	}
	if !seen["uid-a"] || !seen["uid-b"] {
		t.Errorf("got UIDs %v, want both uid-a and uid-b", got)
	}
}

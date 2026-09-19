package cascade

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
}

package score

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	"k8s.io/client-go/rest"

	"github.com/SaiPisey2/sounding/pkg/cluster"
	"github.com/SaiPisey2/sounding/pkg/model"
)

// fakeClients serves core/v1 discovery with one listable namespaced
// resource (configmaps) from an httptest server, and answers the namespace
// Get and the metadata List from client-go's fakes. Discovery's requests go
// through the real counting transport, the others through Score's own
// *c.Calls++, so both halves of the counter are exercised.
func fakeClients(t *testing.T) *cluster.Clients {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Errorf("encoding discovery response: %v", err)
		}
	}
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		write(w, metav1.APIVersions{Versions: []string{"v1"}})
	})
	mux.HandleFunc("/api/v1", func(w http.ResponseWriter, r *http.Request) {
		write(w, metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: metav1.Verbs{"list", "get"}},
		}})
	})
	mux.HandleFunc("/apis", func(w http.ResponseWriter, r *http.Request) {
		write(w, metav1.APIGroupList{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// NewForConfig is what installs the counting transport on discovery;
	// the other three clients are then swapped for fakes.
	c, err := cluster.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("NewForConfig: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c.Metadata = metadatafake.NewSimpleMetadataClient(scheme)
	c.Typed = fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "demo"}})
	c.Dynamic = nil
	return c
}

// Finding.APICalls is what one Score call cost, not the Clients' running
// total: the second of two identical calls on one Clients must report the
// same count as the first, not the first's count plus its own.
func TestAPICallsCountsOnlyThisCall(t *testing.T) {
	c := fakeClients(t)
	act := model.Action{Verb: "delete", Target: model.Target{Resource: "namespaces", Name: "demo"}}
	first, err := Score(context.Background(), c, act, Options{})
	if err != nil {
		t.Fatalf("first Score: %v", err)
	}
	second, err := Score(context.Background(), c, act, Options{})
	if err != nil {
		t.Fatalf("second Score: %v", err)
	}
	if first.APICalls == 0 {
		t.Fatal("first call reported 0 requests; the fake is not being counted")
	}
	if second.APICalls != first.APICalls {
		t.Errorf("second call APICalls = %d, want %d (the same scan again), not a running total", second.APICalls, first.APICalls)
	}
	if got := int(*c.Calls); got != first.APICalls+second.APICalls {
		t.Errorf("counter = %d, want %d: the delta must not reset the shared counter", got, first.APICalls+second.APICalls)
	}
}

func TestCallsSinceIsTheDifference(t *testing.T) {
	n := int64(7)
	c := &cluster.Clients{Calls: &n}
	start := *c.Calls
	*c.Calls += 5
	if got := callsSince(c, start); got != 5 {
		t.Errorf("callsSince = %d, want 5", got)
	}
}

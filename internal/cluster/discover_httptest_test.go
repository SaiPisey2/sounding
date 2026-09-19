package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// newFakeCluster serves just enough of the discovery API for
// ListableNamespaced to walk end to end: the legacy /api and /api/v1
// endpoints for the core group, and /apis plus /apis/<group>/<version> for a
// second group. metricsStatus controls whether that second group answers
// (http.StatusOK) or fails (any other status), which is the one distinction
// this whole task exists to get right: a client-go helper resolves a failing
// group into a real error only when something on the wire actually failed,
// and that is not exercisable through selectListable/wrapDiscoveryError
// alone since neither one talks to a server.
func newFakeCluster(t *testing.T, metricsStatus int) *Clients {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		writeDiscoveryJSON(t, w, metav1.APIVersions{Versions: []string{"v1"}})
	})
	mux.HandleFunc("/api/v1", func(w http.ResponseWriter, r *http.Request) {
		writeDiscoveryJSON(t, w, metav1.APIResourceList{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"list", "get"}},
			},
		})
	})
	mux.HandleFunc("/apis", func(w http.ResponseWriter, r *http.Request) {
		writeDiscoveryJSON(t, w, metav1.APIGroupList{
			Groups: []metav1.APIGroup{{
				Name: "metrics.k8s.io",
				Versions: []metav1.GroupVersionForDiscovery{
					{GroupVersion: "metrics.k8s.io/v1beta1", Version: "v1beta1"},
				},
				PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "metrics.k8s.io/v1beta1", Version: "v1beta1"},
			}},
		})
	})
	mux.HandleFunc("/apis/metrics.k8s.io/v1beta1", func(w http.ResponseWriter, r *http.Request) {
		if metricsStatus != http.StatusOK {
			w.WriteHeader(metricsStatus)
			return
		}
		writeDiscoveryJSON(t, w, metav1.APIResourceList{
			GroupVersion: "metrics.k8s.io/v1beta1",
			APIResources: []metav1.APIResource{
				{Name: "pods", Kind: "PodMetrics", Namespaced: true, Verbs: metav1.Verbs{"list", "get"}},
			},
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dc, err := discovery.NewDiscoveryClientForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("building discovery client against fake server: %v", err)
	}
	var calls int64
	return &Clients{Discovery: dc, Calls: &calls}
}

func writeDiscoveryJSON(t *testing.T, w http.ResponseWriter, v interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encoding fake discovery response: %v", err)
	}
}

// This is the behavior selectListable and wrapDiscoveryError cannot pin on
// their own: a real discovery round trip where one group is unreachable must
// come back through ListableNamespaced as ErrIncompleteDiscovery naming that
// group, not as a shorter but "successful" resource list.
func TestListableNamespacedRefusesWhenAGroupFails(t *testing.T) {
	c := newFakeCluster(t, http.StatusServiceUnavailable)
	_, err := ListableNamespaced(context.Background(), c)
	if err == nil {
		t.Fatal("want an error when an api group does not answer")
	}
	if !errors.Is(err, ErrIncompleteDiscovery) {
		t.Errorf("error %v does not wrap ErrIncompleteDiscovery", err)
	}
	if !strings.Contains(err.Error(), "metrics.k8s.io/v1beta1") {
		t.Errorf("refusal must name the failed group, got %q", err.Error())
	}
}

func TestListableNamespacedSucceedsWhenEveryGroupAnswers(t *testing.T) {
	c := newFakeCluster(t, http.StatusOK)
	got, err := ListableNamespaced(context.Background(), c)
	if err != nil {
		t.Fatalf("ListableNamespaced errored: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d resources, want 2 (core pods and metrics pods)", len(got))
	}
	for _, r := range got {
		if r.GVR.Resource != "pods" {
			t.Errorf("got unexpected resource %+v", r)
		}
	}
}

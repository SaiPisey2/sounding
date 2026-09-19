package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// ListableNamespaced's own count of API calls must reflect what discovery
// actually put on the wire, not the number of resources it came back with:
// ServerPreferredNamespacedResourcesWithContext issues one request to
// enumerate the server's discovery roots plus one per group-version it then
// lists, all invisible to a caller unless something counts them. This is
// checked against a real count of requests the fake server received, not an
// assumption about how many the client library makes for a given input --
// the report's "N api calls" line is worthless if it is a guess with the
// same confidence as a measurement.
func TestListableNamespacedCountsEveryRequestMade(t *testing.T) {
	var serverHits int64
	mux := http.NewServeMux()
	counted := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt64(&serverHits, 1)
			h(w, r)
		}
	}
	mux.HandleFunc("/api", counted(func(w http.ResponseWriter, r *http.Request) {
		writeDiscoveryJSON(t, w, metav1.APIVersions{Versions: []string{"v1"}})
	}))
	mux.HandleFunc("/api/v1", counted(func(w http.ResponseWriter, r *http.Request) {
		writeDiscoveryJSON(t, w, metav1.APIResourceList{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"list", "get"}},
			},
		})
	}))
	mux.HandleFunc("/apis", counted(func(w http.ResponseWriter, r *http.Request) {
		writeDiscoveryJSON(t, w, metav1.APIGroupList{
			Groups: []metav1.APIGroup{{
				Name: "metrics.k8s.io",
				Versions: []metav1.GroupVersionForDiscovery{
					{GroupVersion: "metrics.k8s.io/v1beta1", Version: "v1beta1"},
				},
				PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "metrics.k8s.io/v1beta1", Version: "v1beta1"},
			}},
		})
	}))
	mux.HandleFunc("/apis/metrics.k8s.io/v1beta1", counted(func(w http.ResponseWriter, r *http.Request) {
		writeDiscoveryJSON(t, w, metav1.APIResourceList{
			GroupVersion: "metrics.k8s.io/v1beta1",
			APIResources: []metav1.APIResource{
				{Name: "pods", Kind: "PodMetrics", Namespaced: true, Verbs: metav1.Verbs{"list", "get"}},
			},
		})
	}))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var calls int64
	cfg := &rest.Config{Host: srv.URL, WrapTransport: countingTransport(&calls)}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Fatalf("building discovery client: %v", err)
	}
	c := &Clients{Discovery: dc, Calls: &calls}

	if _, err := ListableNamespaced(context.Background(), c); err != nil {
		t.Fatalf("ListableNamespaced errored: %v", err)
	}

	if calls == 0 {
		t.Fatal("Calls must be nonzero -- discovery definitely made requests")
	}
	if calls != atomic.LoadInt64(&serverHits) {
		t.Errorf("Calls = %d, but the fake server actually received %d requests", calls, serverHits)
	}
}

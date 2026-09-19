// Package cluster is the read-only boundary between sounding and a live
// Kubernetes API server: it builds clients and asks the server what exists.
// Nothing here writes to a cluster; see readonly_test.go, which checks that
// mechanically rather than by review.
package cluster

import (
	"fmt"
	"net/http"
	"sync/atomic"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Clients bundles every client this tool talks to a cluster through, plus a
// shared call counter. The counter lives here, not on each client, so the
// report can say how many requests were made without reaching into three
// different client internals to add them up.
//
// Dynamic exists because a full object body has to be fetched for a GVR this
// tool never compiled against -- whatever discovery listed, including
// CRDs whose Go type is unknown by definition. The typed Clientset can't
// reach those, and hand-building a REST path from a Target's fields would
// have to re-implement the same name/namespace escaping this client already
// gets right.
// Typed is kubernetes.Interface, not the concrete *kubernetes.Clientset
// New builds, so a test can substitute k8s.io/client-go/kubernetes/fake's
// clientset for it without a live server -- every real call site here only
// ever reaches CoreV1(), which the interface already exposes in full.
type Clients struct {
	Discovery *discovery.DiscoveryClient
	Dynamic   dynamic.Interface
	Metadata  metadata.Interface
	Typed     kubernetes.Interface
	Calls     *int64
}

// New builds the clients this tool needs. An empty kubeconfig path means
// "use the default resolution": $KUBECONFIG if set, otherwise
// ~/.kube/config, falling back to the in-cluster config only if neither
// exists -- that is what lets the same binary run from a workstation and
// from inside a pod without a branch here.
//
// clientcmd.BuildConfigFromFlags("", "") does NOT do this, despite reading
// as though it might: with both its arguments empty it skips kubeconfig
// resolution entirely and goes straight to the in-cluster loader, so the
// tool's single most common invocation -- no flag, a normal workstation
// kubeconfig sitting at the default location -- fails outside a pod every
// time, and the error it fails with talks about --master and
// KUBERNETES_MASTER, neither of which is a flag or variable this CLI has.
// NewNonInteractiveDeferredLoadingClientConfig is the client-go entry point
// that actually implements kubectl's own default resolution order, in-
// cluster fallback included.
func New(kubeconfig string) (*Clients, error) {
	var cfg *rest.Config
	var err error
	if kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{},
		).ClientConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("loading cluster config: %w", err)
	}

	var calls int64

	// Only the discovery client's transport is instrumented: cascade,
	// volume and snapshot already increment *Calls themselves once per
	// List/Get they issue against Metadata/Typed/Dynamic, and wrapping
	// every client's transport here as well would count each of those
	// requests twice. Discovery is different -- ListableNamespaced calls
	// a single client-go helper that can silently issue anywhere from a
	// handful to dozens of requests underneath it, with no per-request
	// hook of its own, so this is the only place a true count is
	// available at all.
	discoveryCfg := rest.CopyConfig(cfg)
	discoveryCfg.WrapTransport = countingTransport(&calls)
	dc, err := discovery.NewDiscoveryClientForConfig(discoveryCfg)
	if err != nil {
		return nil, fmt.Errorf("building discovery client: %w", err)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building dynamic client: %w", err)
	}

	mc, err := metadata.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building metadata client: %w", err)
	}

	tc, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building typed client: %w", err)
	}

	return &Clients{
		Discovery: dc,
		Dynamic:   dyn,
		Metadata:  mc,
		Typed:     tc,
		Calls:     &calls,
	}, nil
}

// countingTransport returns a client-go transport wrapper that increments
// calls once for every HTTP request that passes through it. It is what
// makes the "N api calls" line in the report true for discovery specifically:
// counting the resources a scan came back with says nothing about how many
// requests it took to find them.
func countingTransport(calls *int64) func(http.RoundTripper) http.RoundTripper {
	return func(rt http.RoundTripper) http.RoundTripper {
		return &countingRoundTripper{rt: rt, calls: calls}
	}
}

type countingRoundTripper struct {
	rt    http.RoundTripper
	calls *int64
}

func (c *countingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	atomic.AddInt64(c.calls, 1)
	return c.rt.RoundTrip(req)
}

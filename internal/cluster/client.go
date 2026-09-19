// Package cluster is the read-only boundary between sounding and a live
// Kubernetes API server: it builds clients and asks the server what exists.
// Nothing here writes to a cluster; see readonly_test.go, which checks that
// mechanically rather than by review.
package cluster

import (
	"fmt"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/tools/clientcmd"
)

// Clients bundles every client this tool talks to a cluster through, plus a
// shared call counter. The counter lives here, not on each client, so the
// report can say how many requests were made without reaching into three
// different client internals to add them up.
type Clients struct {
	Discovery *discovery.DiscoveryClient
	Metadata  metadata.Interface
	Typed     *kubernetes.Clientset
	Calls     *int64
}

// New builds the three clients this tool needs from a kubeconfig path. An
// empty path is not an error: clientcmd.BuildConfigFromFlags treats an empty
// master URL and an empty path as a request for the in-cluster config, which
// is what lets the same binary run from a workstation and from inside a pod
// without a branch here.
func New(kubeconfig string) (*Clients, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading cluster config: %w", err)
	}

	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building discovery client: %w", err)
	}

	mc, err := metadata.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building metadata client: %w", err)
	}

	tc, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building typed client: %w", err)
	}

	var calls int64
	return &Clients{
		Discovery: dc,
		Metadata:  mc,
		Typed:     tc,
		Calls:     &calls,
	}, nil
}

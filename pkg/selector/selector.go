// Package selector answers what a label or selector change disconnects:
// which Services stop routing to a workload whose pod labels change, and
// how many pods a Service matches before and after its selector changes.
// Neither shows up in a dry-run -- the patch is valid, the cluster accepts
// it, and traffic stops.
package selector

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/SaiPisey2/sounding/pkg/cluster"
)

// Orphaned returns the sorted names of the Services in svcs that select
// before but not after -- the ones a workload's relabelling disconnects.
func Orphaned(svcs []corev1.Service, before, after map[string]string) []string {
	var out []string
	for _, s := range svcs {
		if len(s.Spec.Selector) == 0 {
			continue
		}
		sel := labels.SelectorFromSet(s.Spec.Selector)
		if sel.Matches(labels.Set(before)) && !sel.Matches(labels.Set(after)) {
			out = append(out, s.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Retargeted counts the pods a Service selects before and after its
// selector changes. An empty selector is counted as selecting nothing,
// not everything: labels.SelectorFromSet(empty) matches every pod, but
// Kubernetes stops managing endpoints for a Service with no selector, so
// removing a selector disconnects every pod it routed to.
func Retargeted(pods []corev1.Pod, oldSel, newSel map[string]string) (before, after int) {
	count := func(sel map[string]string) int {
		if len(sel) == 0 {
			return 0
		}
		s, n := labels.SelectorFromSet(sel), 0
		for _, p := range pods {
			if s.Matches(labels.Set(p.Labels)) {
				n++
			}
		}
		return n
	}
	return count(oldSel), count(newSel)
}

// Services lists every Service in ns, for Orphaned to check a workload's
// relabelling against.
func Services(ctx context.Context, c *cluster.Clients, ns string) ([]corev1.Service, error) {
	l, err := c.Typed.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	*c.Calls++
	if err != nil {
		return nil, fmt.Errorf("listing services: %w", err)
	}
	return l.Items, nil
}

// Pods lists every Pod in ns, for Retargeted to check a Service's selector
// change against.
func Pods(ctx context.Context, c *cluster.Clients, ns string) ([]corev1.Pod, error) {
	l, err := c.Typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	*c.Calls++
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}
	return l.Items, nil
}

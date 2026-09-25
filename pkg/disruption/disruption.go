// Package disruption measures what removing pods does to the Services that
// route to them and the PodDisruptionBudgets that protect them: how many
// ready backends each Service keeps, and which budgets the removal breaks.
// It reads EndpointSlices, PDBs and Pods, and nothing else.
package disruption

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/SaiPisey2/sounding/pkg/cluster"
)

// Removal describes a pod removal whose shape is not always known exactly:
// a delete names Pods outright, but a scale-down only says how many of a
// Selector's matches go, never which ones.
type Removal struct {
	Pods     []string          // exact pod names removed (delete)
	Selector map[string]string // or: pods matching this, ...
	Count    int               // ... of which this many are removed (scale down), worst case
}

type Service struct {
	Name        string
	Ready, Left int
}

type Budget struct {
	Name                   string
	Healthy, Desired, Left int
}

type Report struct {
	Services []Service
	Budgets  []Budget
}

// Emptied names every Service that had at least one ready backend and would
// have none left.
func (r Report) Emptied() []string {
	var out []string
	for _, s := range r.Services {
		if s.Ready > 0 && s.Left == 0 {
			out = append(out, s.Name)
		}
	}
	return out
}

// Violated names every budget the removal would push below its own
// DesiredHealthy.
func (r Report) Violated() []string {
	var out []string
	for _, b := range r.Budgets {
		if b.Left < b.Desired {
			out = append(out, b.Name)
		}
	}
	return out
}

// Assess lists the namespace's EndpointSlices, PDBs and Pods -- three
// requests -- and scores rm against them. Any list error is returned: a
// Service this could not read is not a Service with backends to spare.
func Assess(ctx context.Context, c *cluster.Clients, ns string, rm Removal) (Report, error) {
	sl, err := c.Typed.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{})
	*c.Calls++
	if err != nil {
		return Report{}, fmt.Errorf("listing endpointslices: %w", err)
	}
	pl, err := c.Typed.PolicyV1().PodDisruptionBudgets(ns).List(ctx, metav1.ListOptions{})
	*c.Calls++
	if err != nil {
		return Report{}, fmt.Errorf("listing poddisruptionbudgets: %w", err)
	}
	po, err := c.Typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	*c.Calls++
	if err != nil {
		return Report{}, fmt.Errorf("listing pods: %w", err)
	}
	return assess(sl.Items, pl.Items, po.Items, rm), nil
}

func assess(slices []discoveryv1.EndpointSlice, pdbs []policyv1.PodDisruptionBudget, pods []corev1.Pod, rm Removal) Report {
	podLabels := make(map[string]labels.Set, len(pods))
	for _, p := range pods {
		podLabels[p.Name] = labels.Set(p.Labels)
	}
	removed := make(map[string]bool, len(rm.Pods))
	for _, p := range rm.Pods {
		removed[p] = true
	}
	var candidate labels.Selector
	if rm.Selector != nil {
		candidate = labels.SelectorFromSet(rm.Selector)
	}
	// hit counts how many of the given pods this removal takes: exact names
	// for a delete, and for a scale-down the worst case -- as many of the
	// matching pods as Count allows.
	hit := func(names []string) int {
		n := 0
		for _, p := range names {
			if removed[p] {
				n++
			}
		}
		if candidate != nil {
			m := 0
			for _, p := range names {
				if l, ok := podLabels[p]; ok && candidate.Matches(l) {
					m++
				}
			}
			n += min(rm.Count, m)
		}
		return n
	}

	// Ready backends per Service, keyed by pod name so a dual-stack pod --
	// one endpoint in an IPv4 slice and one in an IPv6 slice -- counts
	// once. An endpoint without a Pod targetRef is keyed by its first
	// address; it can never be one of the removed pods.
	ready := map[string]map[string]bool{}
	for _, s := range slices {
		svc := s.Labels[discoveryv1.LabelServiceName]
		if svc == "" {
			continue
		}
		if ready[svc] == nil {
			ready[svc] = map[string]bool{}
		}
		for _, e := range s.Endpoints {
			// discovery/v1: a nil Ready condition means ready.
			if e.Conditions.Ready != nil && !*e.Conditions.Ready {
				continue
			}
			key := ""
			if e.TargetRef != nil && e.TargetRef.Kind == "Pod" {
				key = e.TargetRef.Name
			} else if len(e.Addresses) > 0 {
				key = "addr:" + e.Addresses[0]
			}
			if key != "" {
				ready[svc][key] = true
			}
		}
	}
	var r Report
	for svc, set := range ready {
		names := make([]string, 0, len(set))
		for k := range set {
			names = append(names, k)
		}
		left := len(names) - hit(names)
		r.Services = append(r.Services, Service{Name: svc, Ready: len(names), Left: max(left, 0)})
	}
	for _, b := range pdbs {
		// policy/v1: a nil selector selects no pods; an empty one selects
		// every pod. LabelSelectorAsSelector implements exactly that.
		sel, err := metav1.LabelSelectorAsSelector(b.Spec.Selector)
		if err != nil {
			// An unparseable selector cannot be evaluated; report the
			// budget as violated rather than as safe.
			r.Budgets = append(r.Budgets, Budget{Name: b.Name, Healthy: int(b.Status.CurrentHealthy), Desired: int(b.Status.DesiredHealthy), Left: -1})
			continue
		}
		var matched []string
		for _, p := range pods {
			if sel.Matches(labels.Set(p.Labels)) {
				matched = append(matched, p.Name)
			}
		}
		// hit counts every matching pod this removal takes, but
		// CurrentHealthy already counts only the healthy ones -- so a
		// removed pod that was already unhealthy gets subtracted here
		// though it was never part of Healthy to begin with. That errs
		// toward reporting a violation, never toward hiding one: Left comes
		// out lower than the real number of healthy pods actually at risk,
		// which can only push Left further below Desired, not raise it
		// above a real violation.
		left := int(b.Status.CurrentHealthy) - hit(matched)
		r.Budgets = append(r.Budgets, Budget{Name: b.Name, Healthy: int(b.Status.CurrentHealthy), Desired: int(b.Status.DesiredHealthy), Left: left})
	}
	sort.Slice(r.Services, func(i, j int) bool { return r.Services[i].Name < r.Services[j].Name })
	sort.Slice(r.Budgets, func(i, j int) bool { return r.Budgets[i].Name < r.Budgets[j].Name })
	return r
}

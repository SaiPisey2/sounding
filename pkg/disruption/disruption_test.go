package disruption

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/SaiPisey2/sounding/pkg/cluster"
)

func pod(name string, labels map[string]string) corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func slice(svc string, ready map[string]bool) discoveryv1.EndpointSlice {
	s := discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{discoveryv1.LabelServiceName: svc}}}
	for p, r := range ready {
		r := r
		s.Endpoints = append(s.Endpoints, discoveryv1.Endpoint{
			TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: p},
			Conditions: discoveryv1.EndpointConditions{Ready: &r},
		})
	}
	return s
}

var web = map[string]string{"app": "web"}

func TestDeletingTheOnlyBackendEmptiesTheService(t *testing.T) {
	r := mustAssess(t, []discoveryv1.EndpointSlice{slice("web", map[string]bool{"web-1": true})}, nil,
		[]corev1.Pod{pod("web-1", web)}, Removal{Pods: []string{"web-1"}})
	if !reflect.DeepEqual(r.Emptied(), []string{"web"}) {
		t.Errorf("emptied = %v, want [web]", r.Emptied())
	}
}

func TestOneOfTwoBackendsLeavesOne(t *testing.T) {
	r := mustAssess(t, []discoveryv1.EndpointSlice{slice("web", map[string]bool{"web-1": true, "web-2": true})}, nil,
		[]corev1.Pod{pod("web-1", web), pod("web-2", web)}, Removal{Pods: []string{"web-1"}})
	if len(r.Services) != 1 || r.Services[0].Ready != 2 || r.Services[0].Left != 1 {
		t.Errorf("services = %+v", r.Services)
	}
	if len(r.Emptied()) != 0 {
		t.Errorf("emptied = %v", r.Emptied())
	}
}

// Dual-stack: the same pod is an endpoint in an IPv4 slice and an IPv6 slice.
func TestDualStackPodCountsOnce(t *testing.T) {
	v4 := slice("web", map[string]bool{"web-1": true})
	v6 := slice("web", map[string]bool{"web-1": true})
	r := mustAssess(t, []discoveryv1.EndpointSlice{v4, v6}, nil, []corev1.Pod{pod("web-1", web)}, Removal{Pods: []string{"web-1"}})
	if r.Services[0].Ready != 1 || r.Services[0].Left != 0 {
		t.Errorf("services = %+v, want ready 1 left 0", r.Services)
	}
}

// A nil Ready condition means ready (discovery/v1 API contract).
func TestNilReadyConditionCountsAsReady(t *testing.T) {
	s := discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{discoveryv1.LabelServiceName: "web"}},
		Endpoints: []discoveryv1.Endpoint{{TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: "web-1"}}}}
	r := mustAssess(t, []discoveryv1.EndpointSlice{s}, nil, []corev1.Pod{pod("web-1", web)}, Removal{})
	if r.Services[0].Ready != 1 {
		t.Errorf("ready = %d, want 1", r.Services[0].Ready)
	}
}

func TestNotReadyEndpointsAreNotBackends(t *testing.T) {
	r := mustAssess(t, []discoveryv1.EndpointSlice{slice("web", map[string]bool{"web-1": false, "web-2": true})}, nil,
		[]corev1.Pod{pod("web-1", web), pod("web-2", web)}, Removal{Pods: []string{"web-2"}})
	if r.Services[0].Ready != 1 || r.Services[0].Left != 0 {
		t.Errorf("services = %+v, want ready 1 left 0", r.Services)
	}
}

func TestScaleDownIsScoredWorstCase(t *testing.T) {
	r := mustAssess(t, []discoveryv1.EndpointSlice{slice("web", map[string]bool{"web-1": true, "web-2": true, "web-3": true})}, nil,
		[]corev1.Pod{pod("web-1", web), pod("web-2", web), pod("web-3", web)}, Removal{Selector: &metav1.LabelSelector{MatchLabels: web}, Count: 3})
	if r.Services[0].Left != 0 {
		t.Errorf("scale to zero must leave 0: %+v", r.Services)
	}
	r = mustAssess(t, []discoveryv1.EndpointSlice{slice("web", map[string]bool{"web-1": true, "web-2": true})}, nil,
		[]corev1.Pod{pod("web-1", web), pod("web-2", web)}, Removal{Selector: &metav1.LabelSelector{MatchLabels: web}, Count: 5})
	if r.Services[0].Left != 0 {
		t.Errorf("count above matching pods must floor at 0, not go negative: %+v", r.Services)
	}
}

func pdb(name string, sel *metav1.LabelSelector, healthy, desired int32) policyv1.PodDisruptionBudget {
	return policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:   policyv1.PodDisruptionBudgetSpec{Selector: sel},
		Status: policyv1.PodDisruptionBudgetStatus{CurrentHealthy: healthy, DesiredHealthy: desired}}
}

func TestRemovingPodsBelowDesiredHealthyViolatesTheBudget(t *testing.T) {
	r := mustAssess(t, nil, []policyv1.PodDisruptionBudget{pdb("web-pdb", &metav1.LabelSelector{MatchLabels: web}, 2, 2)},
		[]corev1.Pod{pod("web-1", web), pod("web-2", web)}, Removal{Pods: []string{"web-1"}})
	if !reflect.DeepEqual(r.Violated(), []string{"web-pdb"}) {
		t.Errorf("violated = %v", r.Violated())
	}
}

func TestPDBSelectorNilMatchesNothingEmptyMatchesAll(t *testing.T) {
	pods := []corev1.Pod{pod("web-1", web)}
	r := mustAssess(t, nil, []policyv1.PodDisruptionBudget{
		pdb("nil-sel", nil, 1, 1),
		pdb("empty-sel", &metav1.LabelSelector{}, 1, 1),
	}, pods, Removal{Pods: []string{"web-1"}})
	if !reflect.DeepEqual(r.Violated(), []string{"empty-sel"}) {
		t.Errorf("violated = %v, want only empty-sel", r.Violated())
	}
}

func mustAssess(t *testing.T, slices []discoveryv1.EndpointSlice, pdbs []policyv1.PodDisruptionBudget, pods []corev1.Pod, rm Removal) Report {
	t.Helper()
	r, err := assess(slices, pdbs, pods, rm)
	if err != nil {
		t.Fatalf("assess: %v", err)
	}
	return r
}

// A Count with no Selector would match nothing and report every Service as
// untouched; it must be an error instead.
func TestCountWithNilSelectorIsAnError(t *testing.T) {
	_, err := assess([]discoveryv1.EndpointSlice{slice("web", map[string]bool{"web-1": true})}, nil,
		[]corev1.Pod{pod("web-1", web)}, Removal{Count: 1})
	if err == nil {
		t.Fatal("Count 1 with a nil Selector: want an error, got a report")
	}
	if _, err := Assess(context.Background(), &cluster.Clients{}, "ns", Removal{Count: 1}); err == nil {
		t.Fatal("Assess: Count 1 with a nil Selector: want an error before any request")
	}
}

func TestNegativeCountIsAnError(t *testing.T) {
	_, err := assess(nil, nil, []corev1.Pod{pod("web-1", web)},
		Removal{Selector: &metav1.LabelSelector{MatchLabels: web}, Count: -1})
	if err == nil {
		t.Fatal("negative Count: want an error")
	}
}

func TestAnInvalidSelectorIsAnError(t *testing.T) {
	bad := &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "app", Operator: "Bogus"}}}
	if _, err := assess(nil, nil, nil, Removal{Selector: bad, Count: 1}); err == nil {
		t.Fatal("invalid selector: want an error")
	}
}

// A Deployment whose spec.selector uses only matchExpressions: the worst
// case takes Count of the matching pods and none of the others.
func TestMatchExpressionsSelectorRemovesTheWorstCase(t *testing.T) {
	api := map[string]string{"app": "api"}
	sel := &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
		{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"web"}},
	}}
	r := mustAssess(t, []discoveryv1.EndpointSlice{
		slice("web", map[string]bool{"web-1": true, "web-2": true, "web-3": true}),
		slice("api", map[string]bool{"api-1": true}),
	}, []policyv1.PodDisruptionBudget{pdb("web-pdb", &metav1.LabelSelector{MatchLabels: web}, 3, 2)},
		[]corev1.Pod{pod("web-1", web), pod("web-2", web), pod("web-3", web), pod("api-1", api)},
		Removal{Selector: sel, Count: 2})
	want := []Service{{Name: "api", Ready: 1, Left: 1}, {Name: "web", Ready: 3, Left: 1}}
	if !reflect.DeepEqual(r.Services, want) {
		t.Errorf("services = %+v, want %+v", r.Services, want)
	}
	if !reflect.DeepEqual(r.Violated(), []string{"web-pdb"}) {
		t.Errorf("violated = %v, want [web-pdb] (3 healthy - 2 = 1 < 2)", r.Violated())
	}
}

// A PDB status computed for an older spec cannot vouch for the removal.
func TestAStalePDBStatusIsViolated(t *testing.T) {
	b := pdb("web-pdb", &metav1.LabelSelector{MatchLabels: web}, 5, 1)
	b.Generation = 3
	b.Status.ObservedGeneration = 2
	r := mustAssess(t, nil, []policyv1.PodDisruptionBudget{b},
		[]corev1.Pod{pod("web-1", web), pod("web-2", web)}, Removal{Pods: []string{"web-1"}})
	if !reflect.DeepEqual(r.Violated(), []string{"web-pdb"}) {
		t.Errorf("violated = %v, want [web-pdb] for a stale status", r.Violated())
	}
	b.Status.ObservedGeneration = 3
	r = mustAssess(t, nil, []policyv1.PodDisruptionBudget{b},
		[]corev1.Pod{pod("web-1", web), pod("web-2", web)}, Removal{Pods: []string{"web-1"}})
	if len(r.Violated()) != 0 {
		t.Errorf("violated = %v, want none once the status is current", r.Violated())
	}
}

// Pins current behaviour: an endpoint without a Pod targetRef is keyed by
// its first address, so a dual-stack one is counted once per slice. Left is
// overstated by one; it is never removed, so Emptied is unaffected.
func TestDualStackNonPodEndpointCountsTwice(t *testing.T) {
	ep := func(addr string) discoveryv1.EndpointSlice {
		return discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{discoveryv1.LabelServiceName: "ext"}},
			Endpoints: []discoveryv1.Endpoint{{Addresses: []string{addr}}}}
	}
	r := mustAssess(t, []discoveryv1.EndpointSlice{ep("10.0.0.1"), ep("fd00::1")}, nil, nil, Removal{})
	if r.Services[0].Ready != 2 || r.Services[0].Left != 2 {
		t.Errorf("services = %+v, want ready 2 left 2 (known double count)", r.Services)
	}
}

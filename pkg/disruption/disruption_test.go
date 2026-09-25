package disruption

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	r := assess([]discoveryv1.EndpointSlice{slice("web", map[string]bool{"web-1": true})}, nil,
		[]corev1.Pod{pod("web-1", web)}, Removal{Pods: []string{"web-1"}})
	if !reflect.DeepEqual(r.Emptied(), []string{"web"}) {
		t.Errorf("emptied = %v, want [web]", r.Emptied())
	}
}

func TestOneOfTwoBackendsLeavesOne(t *testing.T) {
	r := assess([]discoveryv1.EndpointSlice{slice("web", map[string]bool{"web-1": true, "web-2": true})}, nil,
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
	r := assess([]discoveryv1.EndpointSlice{v4, v6}, nil, []corev1.Pod{pod("web-1", web)}, Removal{Pods: []string{"web-1"}})
	if r.Services[0].Ready != 1 || r.Services[0].Left != 0 {
		t.Errorf("services = %+v, want ready 1 left 0", r.Services)
	}
}

// A nil Ready condition means ready (discovery/v1 API contract).
func TestNilReadyConditionCountsAsReady(t *testing.T) {
	s := discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{discoveryv1.LabelServiceName: "web"}},
		Endpoints: []discoveryv1.Endpoint{{TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: "web-1"}}}}
	r := assess([]discoveryv1.EndpointSlice{s}, nil, []corev1.Pod{pod("web-1", web)}, Removal{})
	if r.Services[0].Ready != 1 {
		t.Errorf("ready = %d, want 1", r.Services[0].Ready)
	}
}

func TestNotReadyEndpointsAreNotBackends(t *testing.T) {
	r := assess([]discoveryv1.EndpointSlice{slice("web", map[string]bool{"web-1": false, "web-2": true})}, nil,
		[]corev1.Pod{pod("web-1", web), pod("web-2", web)}, Removal{Pods: []string{"web-2"}})
	if r.Services[0].Ready != 1 || r.Services[0].Left != 0 {
		t.Errorf("services = %+v, want ready 1 left 0", r.Services)
	}
}

func TestScaleDownIsScoredWorstCase(t *testing.T) {
	r := assess([]discoveryv1.EndpointSlice{slice("web", map[string]bool{"web-1": true, "web-2": true, "web-3": true})}, nil,
		[]corev1.Pod{pod("web-1", web), pod("web-2", web), pod("web-3", web)}, Removal{Selector: web, Count: 3})
	if r.Services[0].Left != 0 {
		t.Errorf("scale to zero must leave 0: %+v", r.Services)
	}
	r = assess([]discoveryv1.EndpointSlice{slice("web", map[string]bool{"web-1": true, "web-2": true})}, nil,
		[]corev1.Pod{pod("web-1", web), pod("web-2", web)}, Removal{Selector: web, Count: 5})
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
	r := assess(nil, []policyv1.PodDisruptionBudget{pdb("web-pdb", &metav1.LabelSelector{MatchLabels: web}, 2, 2)},
		[]corev1.Pod{pod("web-1", web), pod("web-2", web)}, Removal{Pods: []string{"web-1"}})
	if !reflect.DeepEqual(r.Violated(), []string{"web-pdb"}) {
		t.Errorf("violated = %v", r.Violated())
	}
}

func TestPDBSelectorNilMatchesNothingEmptyMatchesAll(t *testing.T) {
	pods := []corev1.Pod{pod("web-1", web)}
	r := assess(nil, []policyv1.PodDisruptionBudget{
		pdb("nil-sel", nil, 1, 1),
		pdb("empty-sel", &metav1.LabelSelector{}, 1, 1),
	}, pods, Removal{Pods: []string{"web-1"}})
	if !reflect.DeepEqual(r.Violated(), []string{"empty-sel"}) {
		t.Errorf("violated = %v, want only empty-sel", r.Violated())
	}
}

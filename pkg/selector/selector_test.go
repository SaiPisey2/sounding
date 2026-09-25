package selector

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func svc(name string, sel map[string]string) corev1.Service {
	return corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: corev1.ServiceSpec{Selector: sel}}
}

func TestRelabellingATemplateOrphansItsService(t *testing.T) {
	svcs := []corev1.Service{svc("web", map[string]string{"app": "web"}), svc("other", map[string]string{"app": "other"})}
	got := Orphaned(svcs, map[string]string{"app": "web"}, map[string]string{"app": "web2"})
	if !reflect.DeepEqual(got, []string{"web"}) {
		t.Errorf("orphaned = %v, want [web]", got)
	}
}

func TestAddingALabelOrphansNothing(t *testing.T) {
	svcs := []corev1.Service{svc("web", map[string]string{"app": "web"})}
	if got := Orphaned(svcs, map[string]string{"app": "web"}, map[string]string{"app": "web", "tier": "fe"}); len(got) != 0 {
		t.Errorf("orphaned = %v", got)
	}
}

// A Service with no selector has hand-managed endpoints; labels never
// route to it, so no label change can orphan it. SelectorFromSet(empty)
// matches everything, which would report it as matched before and after
// -- harmless here -- but the rule is stated, not left to that accident.
func TestSelectorlessServiceIsNeverOrphaned(t *testing.T) {
	svcs := []corev1.Service{svc("manual", nil)}
	if got := Orphaned(svcs, map[string]string{"app": "web"}, map[string]string{}); len(got) != 0 {
		t.Errorf("orphaned = %v", got)
	}
}

func TestRetargetingASelectorCountsPodsBeforeAndAfter(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "a", Labels: map[string]string{"app": "web"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b", Labels: map[string]string{"app": "web"}}},
	}
	b, a := Retargeted(pods, map[string]string{"app": "web"}, map[string]string{"app": "wbe"})
	if b != 2 || a != 0 {
		t.Errorf("before, after = %d, %d; want 2, 0", b, a)
	}
}

func TestRemovingTheSelectorLeavesNoPods(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "a", Labels: map[string]string{"app": "web"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b", Labels: map[string]string{"app": "web"}}},
	}
	if b, a := Retargeted(pods, map[string]string{"app": "web"}, nil); b != 2 || a != 0 {
		t.Errorf("before, after = %d, %d; want 2, 0", b, a)
	}
}

package cascade

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func gcObj(uid string, owners ...string) Object {
	o := Object{UID: types.UID(uid)}
	o.Target.Name = uid
	for _, w := range owners {
		o.Owners = append(o.Owners, types.UID(w))
	}
	return o
}

func nameSet(objs []Object) map[string]bool {
	m := map[string]bool{}
	for _, o := range objs {
		m[o.Target.Name] = true
	}
	return m
}

func TestDescendantsFollowsAnOwnerChain(t *testing.T) {
	objs := []Object{gcObj("deploy"), gcObj("rs", "deploy"), gcObj("pod-a", "rs"), gcObj("pod-b", "rs"), gcObj("cm")}
	got := nameSet(Descendants(objs, "deploy"))
	for _, want := range []string{"deploy", "rs", "pod-a", "pod-b"} {
		if !got[want] {
			t.Errorf("missing %s: %v", want, got)
		}
	}
	if got["cm"] {
		t.Error("an unrelated object was included")
	}
}

// GC keeps a dependent while any of its owners survives.
func TestDescendantsKeepsAnObjectWithASurvivingOwner(t *testing.T) {
	objs := []Object{gcObj("a"), gcObj("b"), gcObj("shared", "a", "b"), gcObj("only-a", "a")}
	got := nameSet(Descendants(objs, "a"))
	if got["shared"] {
		t.Error("shared has a surviving owner b and must not be destroyed")
	}
	if !got["only-a"] {
		t.Error("only-a's sole owner is deleted; it must be destroyed")
	}
}

// Once b is also in the deleted set, shared goes -- the walk must reach a
// fixpoint, not stop after one pass.
func TestDescendantsReachesAFixpoint(t *testing.T) {
	objs := []Object{gcObj("a"), gcObj("shared", "a", "b"), gcObj("b", "a")}
	if !nameSet(Descendants(objs, "a"))["shared"] {
		t.Error("both owners of shared are deleted; shared must be destroyed")
	}
}

func TestDescendantsSurvivesACycle(t *testing.T) {
	objs := []Object{gcObj("root"), gcObj("x", "root", "y"), gcObj("y", "x")}
	got := Descendants(objs, "root") // must return
	if !nameSet(got)["root"] {
		t.Error("root missing")
	}
}

func TestDescendantsOfAMissingRootIsEmpty(t *testing.T) {
	if got := Descendants([]Object{gcObj("a")}, "nope"); len(got) != 0 {
		t.Errorf("got %d objects for a root that is not present", len(got))
	}
}

// Output order is owner-first, like everything else sounding reports.
func TestDescendantsAreOwnerFirst(t *testing.T) {
	objs := []Object{gcObj("pod", "rs"), gcObj("rs", "deploy"), gcObj("deploy")}
	got := Descendants(objs, "deploy")
	if len(got) != 3 || got[0].Target.Name != "deploy" || got[1].Target.Name != "rs" || got[2].Target.Name != "pod" {
		t.Errorf("order = %v", got)
	}
}

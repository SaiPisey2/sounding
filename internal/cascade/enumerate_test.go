package cascade

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/SaiPisey2/sounding/internal/model"
)

func obj(name string, uid types.UID, owners ...types.UID) Object {
	return Object{Target: model.Target{Name: name}, UID: uid, Owners: owners}
}

func TestOrderPutsOwnersBeforeOwned(t *testing.T) {
	// deployment -> replicaset -> pod, supplied deliberately out of order.
	in := []Object{
		obj("pod", "3", "2"),
		obj("deploy", "1"),
		obj("rs", "2", "1"),
	}
	got := Order(in)
	pos := map[string]int{}
	for i, o := range got {
		pos[o.Target.Name] = i
	}
	if !(pos["deploy"] < pos["rs"] && pos["rs"] < pos["pod"]) {
		t.Errorf("order = %v, want deploy before rs before pod", names(got))
	}
}

// An ownerReference pointing outside the namespace, or at something already
// gone, must not drop the object from the report. Under-reporting is the
// failure that matters here.
func TestOrderKeepsObjectsWithDanglingOwners(t *testing.T) {
	in := []Object{obj("orphan", "9", "does-not-exist")}
	if got := Order(in); len(got) != 1 {
		t.Fatalf("got %d objects, want the orphan kept", len(got))
	}
}

func TestOrderIsStableForUnrelatedObjects(t *testing.T) {
	in := []Object{obj("a", "1"), obj("b", "2"), obj("c", "3")}
	first := names(Order(in))
	for i := 0; i < 20; i++ {
		if got := names(Order(in)); !equal(got, first) {
			t.Fatalf("order changed between runs: %v then %v", first, got)
		}
	}
}

func TestOrderTerminatesOnACycle(t *testing.T) {
	// Two objects owning each other should not hang or drop anything.
	in := []Object{obj("x", "1", "2"), obj("y", "2", "1")}
	if got := Order(in); len(got) != 2 {
		t.Fatalf("got %d objects, want 2", len(got))
	}
}

// Order takes a plain, caller-built slice, and this tool cannot assume a
// real cluster's UID uniqueness holds for it. Collapsing two distinct
// objects that happen to share a UID down to one is a silent drop from the
// blast radius -- the one failure this package must never produce.
func TestOrderKeepsAllObjectsSharingAUID(t *testing.T) {
	in := []Object{obj("first", "1"), obj("second", "1")}
	got := Order(in)
	if len(got) != 2 {
		t.Fatalf("got %d objects, want 2 (both share UID \"1\")", len(got))
	}
	seen := map[string]bool{}
	for _, o := range got {
		seen[o.Target.Name] = true
	}
	if !seen["first"] || !seen["second"] {
		t.Fatalf("got %v, want both first and second", names(got))
	}
}

// TestOrderIsStableForUnrelatedObjects only proves stability across repeated
// calls with one fixed input slice. Its fixture has no owners, so plain
// slice-insertion order already reproduces itself call to call, and that
// test still passes even with the UID sort on roots deleted. What Order
// actually needs to survive is the same logical objects arriving in a
// *different* slice order -- which two List() calls are not obliged to
// avoid -- and only the UID sort makes that case reproducible.
func TestOrderIsStableAcrossInputOrder(t *testing.T) {
	a, b, c := obj("a", "1"), obj("b", "2"), obj("c", "3")
	first := names(Order([]Object{a, b, c}))
	second := names(Order([]Object{c, a, b}))
	third := names(Order([]Object{b, c, a}))
	if !equal(first, second) || !equal(first, third) {
		t.Fatalf("order depends on input order: %v, %v, %v", first, second, third)
	}
}

func names(objs []Object) []string {
	var out []string
	for _, o := range objs {
		out = append(out, o.Target.Name)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

package cascade

import "k8s.io/apimachinery/pkg/types"

// Descendants returns root and every object the garbage collector would
// delete after root is deleted with background propagation, owner-first.
//
// The rule is Kubernetes' own: a dependent is collected only when every
// owner it lists is gone. An owner not present in objs -- cluster-scoped,
// or already deleted -- is treated as surviving, because this function
// cannot see it go. That can understate only for a dependent whose other
// owner was already gone before the delete, which the GC would already
// have collected; it never counts a survivor as destroyed. The same holds
// for a namespaced owner of a kind that is not listable (no "list" verb, so
// cascade.Enumerate never saw it): it is absent from objs, so it is treated
// as surviving, and anything it co-owns is kept.
//
// It iterates to a fixpoint rather than walking children once, because an
// object with two owners becomes deletable only after the second owner is
// itself found deletable, which a single pass in the wrong order misses.
func Descendants(objs []Object, root types.UID) []Object {
	present := false
	for _, o := range objs {
		if o.UID == root {
			present = true
			break
		}
	}
	if !present {
		return nil
	}
	deleted := map[types.UID]bool{root: true}
	for changed := true; changed; {
		changed = false
		for _, o := range objs {
			if deleted[o.UID] || len(o.Owners) == 0 {
				continue
			}
			all := true
			for _, w := range o.Owners {
				if !deleted[w] {
					all = false
					break
				}
			}
			if all {
				deleted[o.UID] = true
				changed = true
			}
		}
	}
	var out []Object
	for _, o := range objs {
		if deleted[o.UID] {
			out = append(out, o)
		}
	}
	return Order(out)
}

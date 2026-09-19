// Package cascade walks a namespace and builds the ownership graph that the
// rest of sounding reasons about: what exists, and what would fall with it.
package cascade

import (
	"context"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/SaiPisey2/sounding/internal/cluster"
	"github.com/SaiPisey2/sounding/internal/model"
)

// Object is one live object as sounding sees it: enough to place it in the
// ownership graph and enough to name it in a report or a restore plan.
type Object struct {
	Target     model.Target
	UID        types.UID
	Owners     []types.UID
	Finalizers []string
}

// Enumerate lists every object in ns across every listable namespaced
// resource. The metadata client returns only ObjectMeta -- no group,
// version, resource or kind -- so those four fields come from the
// cluster.Resource this list call was made for, not from the object itself.
// Downstream code matches on Target.Resource (e.g. "persistentvolumeclaims")
// and prints Kind/Name; leaving any of those four zero makes that match
// silently fail while the tool still appears to have run.
//
// Any list error aborts and is returned rather than folded into a shorter
// result: a partial enumeration would understate the blast radius, and this
// tool would rather refuse than report a namespace as safer than it is.
func Enumerate(ctx context.Context, c *cluster.Clients, rs []cluster.Resource, ns string) ([]Object, error) {
	var out []Object
	for _, r := range rs {
		pl, err := c.Metadata.Resource(r.GVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		*c.Calls++
		if err != nil {
			return nil, err
		}
		for _, item := range pl.Items {
			var owners []types.UID
			for _, o := range item.OwnerReferences {
				owners = append(owners, o.UID)
			}
			out = append(out, Object{
				Target: model.Target{
					Group:     r.GVR.Group,
					Version:   r.GVR.Version,
					Resource:  r.GVR.Resource,
					Kind:      r.Kind,
					Namespace: item.Namespace,
					Name:      item.Name,
				},
				UID:        item.UID,
				Owners:     owners,
				Finalizers: item.Finalizers,
			})
		}
	}
	return out, nil
}

// Order returns objects owner-before-owned, so a report and a restore read
// in the same direction: recreate what appears first, first.
//
// It walks depth-first from every object whose owners are not present in
// this set -- namespace roots, and objects whose owners already left the
// namespace or the cluster -- appending each object before the children that
// point back at it. Owner references that never resolve (dangling owners)
// or that resolve in a loop (cycles) must not make an object disappear from
// the report, so anything the walk never reaches is appended afterward, in
// UID order for determinism across runs.
func Order(objs []Object) []Object {
	byUID := make(map[types.UID]Object, len(objs))
	childrenOf := make(map[types.UID][]types.UID)
	hasOwnerInSet := make(map[types.UID]bool, len(objs))

	for _, o := range objs {
		byUID[o.UID] = o
	}
	for _, o := range objs {
		for _, ownerUID := range o.Owners {
			if _, ok := byUID[ownerUID]; ok {
				childrenOf[ownerUID] = append(childrenOf[ownerUID], o.UID)
				hasOwnerInSet[o.UID] = true
			}
		}
	}
	// Children lists are built by ranging over objs, so they already come
	// out in input order; sorting by UID on top of that is what makes
	// TestOrderIsStableForUnrelatedObjects hold across repeated calls
	// regardless of map iteration order elsewhere in this function.
	for uid := range childrenOf {
		kids := childrenOf[uid]
		sort.Slice(kids, func(i, j int) bool { return kids[i] < kids[j] })
		childrenOf[uid] = kids
	}

	var roots []types.UID
	for _, o := range objs {
		if !hasOwnerInSet[o.UID] {
			roots = append(roots, o.UID)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i] < roots[j] })

	visited := make(map[types.UID]bool, len(objs))
	out := make([]Object, 0, len(objs))

	var walk func(uid types.UID)
	walk = func(uid types.UID) {
		if visited[uid] {
			// A cycle closes back on an ancestor still being walked; without
			// this guard the recursion never returns.
			return
		}
		visited[uid] = true
		out = append(out, byUID[uid])
		for _, child := range childrenOf[uid] {
			walk(child)
		}
	}
	for _, root := range roots {
		walk(root)
	}

	// Anything left unvisited is a cycle with no member outside it (every
	// member has an owner inside the set, so none qualified as a root).
	// Appending it in UID order still surfaces it instead of dropping it.
	var stragglers []types.UID
	for _, o := range objs {
		if !visited[o.UID] {
			stragglers = append(stragglers, o.UID)
		}
	}
	sort.Slice(stragglers, func(i, j int) bool { return stragglers[i] < stragglers[j] })
	for _, uid := range stragglers {
		walk(uid)
	}

	return out
}

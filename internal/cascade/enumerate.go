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
//
// A conformant cluster serves some kinds under more than one group -- Events
// under both "v1" and "events.k8s.io/v1" is the one every cluster since 1.19
// has -- and ListableNamespaced has no way to know that ahead of time, so it
// lists both. Without deduplication that reads the same object twice, and
// the count this tool exists to get exactly right comes out wrong by
// however many resources double-serve. seen is keyed by UID, not by
// group/kind or resource name, because the rule is "one object, one UID,
// appears once" for whichever pair of groups happens to double-serve it
// next -- a carve-out for Events specifically would leave the next
// dual-served resource broken the same way.
//
// This sits upstream of, and solves a different problem from, Order's
// byUID map[types.UID][]Object: that map keeps every object sharing a UID
// because Order cannot assume its caller-supplied input has real cluster
// UID uniqueness. Here the assumption runs the other way -- a UID really
// does name exactly one object -- and that assumption is what licenses
// collapsing the one case where our own listing, not the input, produced
// the duplicate: the same object read twice through two list calls.
func Enumerate(ctx context.Context, c *cluster.Clients, rs []cluster.Resource, ns string) ([]Object, error) {
	var out []Object
	seen := make(map[types.UID]bool)
	for _, r := range rs {
		pl, err := c.Metadata.Resource(r.GVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		// The call happened and cost the caller a request regardless of how
		// many of its results turn out to be duplicates already seen under
		// another group, so it counts here, before the dedupe check below.
		*c.Calls++
		if err != nil {
			return nil, err
		}
		for _, item := range pl.Items {
			if seen[item.UID] {
				continue
			}
			seen[item.UID] = true
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
	// byUID holds a slice, not a single Object, because objs is caller-
	// supplied and this tool cannot assume a real cluster's UID uniqueness
	// holds for it. Collapsing two objects that share a UID down to one
	// silently removes an object from the blast radius -- the one outcome
	// this package must never produce, however the bad input arrives.
	byUID := make(map[types.UID][]Object, len(objs))
	childrenOf := make(map[types.UID][]types.UID)
	hasOwnerInSet := make(map[types.UID]bool, len(objs))

	for _, o := range objs {
		byUID[o.UID] = append(byUID[o.UID], o)
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
		out = append(out, byUID[uid]...)
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

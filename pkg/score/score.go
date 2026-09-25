// Package score turns an action into a Finding against a live cluster. It
// is the one place the class-composition rule lives --
// Classify(effects, max(floorFor, volumeClass)) -- so the sounding CLI and
// any other front end that imports this package cannot disagree about what
// an action is. Before this package existed the rule sat in the CLI's main,
// and a second front end would have had to copy it; a copy that dropped
// the verb floor would classify a namespace of destroyed objects as READ.
package score

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/SaiPisey2/sounding/pkg/cascade"
	"github.com/SaiPisey2/sounding/pkg/cluster"
	"github.com/SaiPisey2/sounding/pkg/model"
	"github.com/SaiPisey2/sounding/pkg/snapshot"
	"github.com/SaiPisey2/sounding/pkg/volume"
)

// ErrRefused and ErrOperational are the two non-success outcomes this
// command can produce, and they are kept distinct because they tell a
// caller different things. A refusal means the COMMAND could not be
// scored -- bad input, a resource discovery cannot identify, a cluster
// that cannot be fully enumerated -- and is worth fixing the command for.
// An operational error means sounding itself failed to run -- a bad
// kubeconfig, a network error, a disk write failure -- and is worth fixing
// the environment for. Collapsing the two into one exit code would leave a
// caller's retry logic guessing which kind of failure it is looking at.
var (
	ErrRefused     = errors.New("refused")
	ErrOperational = errors.New("operational error")
)

// Options carries the flags Score's own behaviour is conditioned on,
// separately from the cluster clients and the action being scored.
type Options struct{ SnapshotDir string }

// Score builds a Finding for act against a live cluster. The class it
// returns depends only on what enumeration and the volume join found --
// never on whether snapshotDir is set. snapshotDir is consulted only AFTER
// the Finding (and therefore its Class) already exists, and its only
// effect is to fill in Undo, a field Classify never looks at. That
// ordering is what makes "the flag cannot change the verdict" true by
// construction rather than by the coincidence of nobody having wired a
// snapshot-taken signal into Classify yet -- a coincidence a future edit
// could break without anyone noticing, right up until two runs of the same
// command, one with --snapshot and one without, printed two different
// verdicts.
//
// act.Target.Group, when set, restricts which API group the target resource
// is resolved in (see resolveTarget); nothing in that group matching the
// resource is ErrRefused, never a fallback to another group.
//
// Finding.APICalls is the number of requests THIS call made: c.Calls is a
// running total over the Clients' life, and Score reports the difference
// between its value on return and on entry. That makes it correct for
// sequential calls on one Clients only. A Clients is not safe for
// concurrent scoring -- the counter is incremented without synchronisation
// outside discovery, and two overlapping calls would each count the other's
// requests -- so a caller scoring concurrently needs one Clients per
// goroutine (cluster.NewForConfig is cheap to call again).
func Score(ctx context.Context, c *cluster.Clients, act model.Action, opts Options) (model.Finding, error) {
	// Captured before any request is made, not after enumeration finishes.
	// The report's whole point in stating this is letting the caller judge
	// drift between what was observed and when they act on it; stamping it
	// after the scan's slowest, most-request-heavy phase already ran would
	// under-report exactly the window that matters most on a large cluster.
	scanned := time.Now().UTC()
	callsAtStart := *c.Calls

	resources, err := cluster.ListableNamespaced(ctx, c)
	if err != nil {
		if errors.Is(err, cluster.ErrIncompleteDiscovery) {
			// A cluster this tool cannot fully enumerate must never be
			// scored as if it were small. Refusing, rather than reporting
			// whatever partial list came back, is the same rule
			// cluster.ListableNamespaced itself already enforces; this is
			// just where that refusal surfaces as an exit code.
			return model.Finding{}, fmt.Errorf("%w: %w", ErrRefused, err)
		}
		return model.Finding{}, fmt.Errorf("%w: listing namespaced resources: %v", ErrOperational, err)
	}

	// resolveTarget's errors already wrap ErrRefused, with the same text
	// this path printed before object deletes were scored; wrapping them a
	// second time here would print "refused: refused: ...".
	tg, err := resolveTarget(act, resources)
	if err != nil {
		return model.Finding{}, err
	}
	ns := tg.namespace

	if err := namespaceMustExist(ctx, c, ns); err != nil {
		return model.Finding{}, err
	}

	objs, err := cascade.Enumerate(ctx, c, resources, ns)
	if err != nil {
		return model.Finding{}, fmt.Errorf("%w: enumerating namespace %q: %v", ErrOperational, ns, err)
	}

	replaced, replacedBy := false, ""
	var rootTarget model.Target
	explain := func(cascade.Object) string { return "in the namespace" }
	if tg.object != nil {
		var root *cascade.Object
		for i := range objs {
			o := &objs[i]
			if o.Target.Group == tg.object.GVR.Group && o.Target.Resource == tg.object.GVR.Resource && o.Target.Name == tg.name {
				root = o
				break
			}
		}
		if root == nil {
			// Scoring an absent object as an empty cascade would print a
			// confident verdict for a command kubectl itself rejects with
			// NotFound -- the object-level twin of namespaceMustExist.
			return model.Finding{}, fmt.Errorf("%w: %s/%s does not exist in namespace %q", ErrRefused, tg.object.GVR.Resource, tg.name, tg.namespace)
		}
		rootUID := root.UID
		rootTarget = root.Target
		all := objs
		// Everything past this point -- the effects, the volume join, the
		// snapshot -- sees only what the garbage collector takes with the
		// root. A PVC in the namespace that the target does not own is not
		// destroyed by this delete, and naming its PV's reclaim policy would
		// grade the command by data it never touches.
		objs = cascade.Descendants(all, rootUID)
		replacedBy = controllerThatRecreates(*root, all, objs)
		replaced = replacedBy != ""
		explain = func(o cascade.Object) string {
			if o.UID == rootUID {
				return "the target"
			}
			return "owned by the target; removed by garbage collection"
		}
	}

	effects := destroyEffectsFromObjects(objs, explain)
	if replaced {
		effects = append(effects, model.Effect{
			Kind: "replaced", Basis: model.BasisComputed, Object: rootTarget,
			Explanation: "recreated by its controller " + replacedBy,
		})
	}

	volEffects, volClass, err := volume.Join(ctx, c, objs)
	if err != nil {
		return model.Finding{}, fmt.Errorf("%w: joining persistent volumes: %v", ErrOperational, err)
	}
	effects = append(effects, volEffects...)

	finding, err := classify(act, effects, volClass, replaced)
	if err != nil {
		// resolveTarget already refuses every verb but "delete" before
		// this point is ever reached, so this is not a real user-facing
		// case today -- it is floorFor's own defence against a second
		// verb being wired into resolveTarget without anyone adding a
		// matching case here, which would otherwise silently inherit
		// "delete namespace"'s floor for a verb that has never been
		// analysed.
		return model.Finding{}, fmt.Errorf("%w: %v", ErrOperational, err)
	}
	finding.Scanned = scanned
	finding.APICalls = callsSince(c, callsAtStart)

	if opts.SnapshotDir != "" {
		plan, err := snapshot.Write(ctx, c, opts.SnapshotDir, objs, excludedFromEffects(volEffects))
		if err != nil {
			return model.Finding{}, fmt.Errorf("%w: writing snapshot: %v", ErrOperational, err)
		}
		finding.Undo = plan
		// snapshot.Write issues its own Get calls; the count the report
		// prints must reflect what the scan actually cost, not just the
		// enumeration and volume-join calls made before it.
		finding.APICalls = callsSince(c, callsAtStart)
	}

	return finding, nil
}

// callsSince is how many requests c has counted since it read start. It is
// what keeps Finding.APICalls to one Score call's cost when a caller reuses
// a Clients for a second action, rather than the sum of every call so far.
func callsSince(c *cluster.Clients, start int64) int {
	return int(*c.Calls - start)
}

// classify composes the final class from the verb's own floor and the
// worst class the volume join found, then builds the Finding through the
// package's one constructor. It is pulled out of score as its own function
// so the composition rule -- floor := max(floorFor(act.Verb, replaced),
// volClass) -- can be exercised directly, without a live cluster, by a test
// that would otherwise have no way to catch a regression here: bypassing
// floorFor in
// favour of model.ClassRead compiles cleanly and only shows up as every
// namespace deletion reporting the single most permissive class there is.
//
// replaced lowers only the verb's floor, never volClass: a Pod its
// controller recreates is REVERSIBLE as an object, but if what it takes
// with it includes a claim whose data cannot come back, the volume join's
// TERMINAL still wins.
func classify(act model.Action, effects []model.Effect, volClass model.Class, replaced bool) (model.Finding, error) {
	floor, err := floorFor(act.Verb, replaced)
	if err != nil {
		return model.Finding{}, err
	}
	if volClass > floor {
		floor = volClass
	}
	return model.NewFinding(act, effects, floor), nil
}

// floorFor is the worst class this build will ever report for verb,
// regardless of what Classify's basis rules alone would allow. Classify
// only ever raises a finding to the floor each EFFECT's basis imposes --
// it has no notion of what the verb being scored actually does. Every
// effect this analyzer produces from enumeration carries BasisComputed,
// whose own floor is ClassRead, the most permissive class there is, so
// without a verb floor supplied from outside model.Classify, deleting a
// namespace full of genuinely measured, destructive effects would print as
// safe to read. That is not merely a wrong answer -- it is the exact
// failure this project exists to prevent, one level up: an unexamined
// default standing in for a judgement, and printing with the full
// credibility of a measurement.
//
// This takes verb and requires an explicit case per one, rather than being
// a single constant every caller reuses, on purpose: a second verb reaching
// this function without a case of its own is refused instead of silently
// inheriting "delete"'s floor for behaviour nobody has actually analysed.
//
// replacedByController is the one fact about the target, rather than the
// verb, that moves the floor: deleting an object its controller recreates
// is undone by the cluster itself, and grading it COMPENSABLE would make a
// gate hold every routine pod restart.
func floorFor(verb string, replacedByController bool) (model.Class, error) {
	switch verb {
	case "delete":
		if replacedByController {
			// The object's controller recreates it -- a Pod under a
			// ReplicaSet or StatefulSet. Nothing is lost that the cluster
			// does not put back by itself.
			return model.ClassReversible, nil
		}
		// The objects a deletion destroys ARE restorable from a
		// snapshot even when the data behind some of them is not, which is
		// why this is ClassCompensable and not ClassTerminal -- volume.Join
		// is what raises the floor further, per object, when data
		// specifically cannot come back.
		return model.ClassCompensable, nil
	default:
		return 0, fmt.Errorf("verb %q has no floor defined", verb)
	}
}

// target is what an action resolves to before anything is enumerated:
// always a namespace, plus, for an object delete, the resource and name
// of the one object at the root of the cascade.
type target struct {
	namespace string
	object    *cluster.Resource // nil for "delete namespace"
	name      string
}

// resolveTarget decides what act would tear down, or refuses. Two shapes
// are understood end to end: "delete namespace <name>", whose cascade is
// everything cascade.Enumerate lists in that namespace, and
// "delete <namespaced-resource> <name> -n <ns>", whose cascade is the
// object plus whatever cascade.Descendants says the garbage collector
// takes with it. Every error it returns already wraps ErrRefused, so
// Score returns them as they are.
//
// Namespace itself is deliberately absent from resources: it is a
// cluster-scoped kind, ListableNamespaced only ever reports namespaced
// ones, and no amount of resolving against that list will ever produce it.
// So it is recognised directly, by name, rather than through
// cluster.ResolveResource. The same fact is what refuses every other
// cluster-scoped kind (R8): it never appears in resources, so
// ResolveResource refuses it rather than scoring a delete whose cascade
// this package cannot enumerate. Every OTHER resource string -- typed
// exactly as the caller wrote it, since action.ParseCommand no longer
// guesses a plural -- is resolved against live discovery before it is
// used for anything, including before it is named in a refusal, so a
// caller is refused with the resource's real name and, when the string is
// genuinely ambiguous, the real candidates -- never a plural nobody typed
// that happens to match nothing.
//
// act.Target.Group, when set, restricts that resolution to the named API
// group. The empty string means "any group", not "the core group": core
// cannot be named on its own, so a resource string core shares with
// another group stays ambiguous unless the other group is named.
func resolveTarget(act model.Action, resources []cluster.Resource) (target, error) {
	if act.Verb != "delete" {
		return target{}, fmt.Errorf("%w: verb %q has no analyzer", ErrRefused, act.Verb)
	}
	if isNamespaceResource(act.Target.Resource) {
		// Namespace is core/v1; a caller that names any other group
		// means some other resource, and scoring a namespace delete for
		// it would grade a command they did not write.
		if act.Target.Group != "" {
			return target{}, fmt.Errorf("%w: no resource in group %q matches %q", ErrRefused, act.Target.Group, act.Target.Resource)
		}
		if act.Target.Name == "" {
			return target{}, fmt.Errorf("%w: delete namespace requires a name", ErrRefused)
		}
		return target{namespace: act.Target.Name}, nil
	}
	// A caller that names the group has already said which of two
	// same-named resources it means -- core "events" or events.k8s.io
	// "events" -- so only that group's resources are candidates. Nothing
	// in the group matching is a refusal, never a fallback to another
	// group's resource of the same name.
	if act.Target.Group != "" {
		var inGroup []cluster.Resource
		for _, r := range resources {
			if r.GVR.Group == act.Target.Group {
				inGroup = append(inGroup, r)
			}
		}
		if len(inGroup) == 0 {
			return target{}, fmt.Errorf("%w: no resource in group %q matches %q", ErrRefused, act.Target.Group, act.Target.Resource)
		}
		resources = inGroup
	}
	resolved, err := cluster.ResolveResource(resources, act.Target.Resource)
	if err != nil {
		return target{}, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	if act.Target.Name == "" {
		return target{}, fmt.Errorf("%w: delete %s requires a name", ErrRefused, resolved.GVR.Resource)
	}
	// kubectl would fall back to the kubeconfig context's namespace. That
	// is state this package never sees, and guessing "default" would score
	// a different object than the one the command deletes.
	if act.Target.Namespace == "" {
		return target{}, fmt.Errorf("%w: delete %s/%s requires a namespace (-n)", ErrRefused, resolved.GVR.Resource, act.Target.Name)
	}
	return target{namespace: act.Target.Namespace, object: &resolved, name: act.Target.Name}, nil
}

// recreatingControllers are the owner kinds, by the apiVersion and kind an
// ownerReference names, that keep a replica count and put an equivalent
// Pod back when one of theirs is deleted. It is an allowlist on purpose:
// "a surviving controller" alone scored a CronJob's Job, a Deployment's
// old ReplicaSet, a Job's finished Pod and an operator's Secret REVERSIBLE,
// and none of those comes back as it was.
var recreatingControllers = map[[2]string]bool{
	{"apps/v1", "ReplicaSet"}:       true,
	{"apps/v1", "StatefulSet"}:      true,
	{"apps/v1", "DaemonSet"}:        true,
	{"v1", "ReplicationController"}: true,
}

// controllerThatRecreates names the controller that will put root back
// after it is deleted, or "" when nothing will. Only a core Pod qualifies,
// and only when its controller reference is one of recreatingControllers
// and that controller is in the namespace, is not terminating, and is not
// deleted along with root. A controller UID that is not in the namespace
// is not taken on trust: this package cannot see it, so it cannot say it
// will recreate anything.
func controllerThatRecreates(root cascade.Object, all, deleted []cascade.Object) string {
	if root.Target.Group != "" || root.Target.Resource != "pods" || root.Controller == "" {
		return ""
	}
	if !recreatingControllers[[2]string{root.ControllerAPIVersion, root.ControllerKind}] {
		return ""
	}
	for _, o := range deleted {
		if o.UID == root.Controller {
			return ""
		}
	}
	group := ""
	if i := strings.Index(root.ControllerAPIVersion, "/"); i >= 0 {
		group = root.ControllerAPIVersion[:i]
	}
	for _, o := range all {
		if o.UID != root.Controller {
			continue
		}
		// The object behind the UID must be the kind the reference
		// claims; a mismatch is a reference this package cannot vouch for.
		if o.Terminating || o.Target.Kind != root.ControllerKind || o.Target.Group != group {
			return ""
		}
		return o.Target.Kind + "/" + o.Target.Name
	}
	return ""
}

// namespaceMustExist refuses before any enumeration happens if ns does not
// exist on the live cluster. Without this, "does not exist" and "exists and
// is genuinely empty" are indistinguishable to cascade.Enumerate -- both
// list zero objects across zero kinds -- so a typo'd namespace name would
// score COMPENSABLE with a confident, scoreable verdict and exit 3, which a
// gateway thresholding on exit code reads as a real recoverable deletion.
// The two objects Kubernetes auto-creates in every real namespace (the
// "default" ServiceAccount and the kube-root-ca.crt ConfigMap) are exactly
// what would tell enumeration a namespace is real rather than absent, and
// nothing before this checked for them. Get on the namespace object itself
// is the one call that actually distinguishes the two cases, rather than
// inferring it from what enumeration happened to find inside.
func namespaceMustExist(ctx context.Context, c *cluster.Clients, ns string) error {
	_, err := c.Typed.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	*c.Calls++
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: namespace %q does not exist", ErrRefused, ns)
	}
	if err != nil {
		return fmt.Errorf("%w: checking namespace %q: %v", ErrOperational, ns, err)
	}
	return nil
}

func isNamespaceResource(s string) bool {
	switch strings.ToLower(s) {
	case "namespace", "namespaces", "ns":
		return true
	}
	return false
}

// destroyEffectsFromObjects converts the objects a delete takes into
// "destroys" effects, owner-first, with explain supplying each one's
// reason: "in the namespace" for a namespace delete, and for an object
// delete whether it is the target or goes by garbage collection. It orders objs itself, via
// cascade.Order, rather than trusting the caller to have already done so:
// cascade.Enumerate's own output order is whatever
// cluster.ListableNamespaced's resource list happens to be in, sorted
// lexicographically by GroupVersionResource string -- and "" (the core
// group) sorts before "apps", so on a real namespace a Pod would print
// before the Deployment that owns it, and the report's effect cap would
// push the actual owner chain out of what a reader ever sees. This mirrors
// what snapshot.Write already does to the same objects when writing the
// restore ordering; without it the report and the undo bundle disagree
// about the same cascade.
//
// Pulling this into its own function, separate from cascade.Order's own
// exhaustive test suite, is what lets a test pin that the ORDERING CALL
// actually happens here -- cascade.Order can be perfectly correct and
// still never get invoked, and nothing in cascade's own tests can catch
// that, because they call Order directly.
func destroyEffectsFromObjects(objs []cascade.Object, explain func(cascade.Object) string) []model.Effect {
	ordered := cascade.Order(objs)
	effects := make([]model.Effect, 0, len(ordered))
	for _, o := range ordered {
		effects = append(effects, model.Effect{
			Kind:  "destroys",
			Basis: model.BasisComputed,
			Object: model.Target{
				Group: o.Target.Group, Version: o.Target.Version, Resource: o.Target.Resource,
				Kind: o.Target.Kind, Namespace: o.Target.Namespace, Name: o.Target.Name,
			},
			Explanation: explain(o),
		})
	}
	return effects
}

// excludedFromEffects lists, in the words the undo bundle's NOT-RESTORED.txt
// wants, every effect whose OBJECT is genuinely absent from the bundle --
// not every effect volume.Join produced about uncertain data. destroys-data
// and detaches-data always name the bound PersistentVolume (see
// pkg/volume/join.go's classifyPV), which is cluster-scoped and
// therefore never one of the objects cascade.Enumerate walks or
// snapshot.Write captures -- genuinely absent either way, whether the
// reclaim policy destroys the data (destroys-data) or merely strands it
// (detaches-data, whose claimRef still needs clearing by hand before the
// restored PVC can rebind).
//
// unknown-data-fate is NOT always about an absent object, and that used to
// be missed: classifyPV emits it for a bound PV with an unrecognised
// reclaim policy (Object = the PV, absent, same as the two cases above),
// but classifyUnbound emits the identical Kind for a PVC with no
// spec.volumeName -- and THAT effect's Object is the claim itself, a
// namespaced object cascade.Enumerate already walked and snapshot.Write
// already wrote to the bundle like any other object in the namespace.
// Treating every unknown-data-fate effect the same regardless of which
// function produced it put an unbound PVC's name in NOT-RESTORED.txt while
// persistentvolumeclaims-<name>.json sat in the same directory and
// restore.sh applied it -- the one file whose entire job is saying what a
// restore will not bring back, wrong about the one line an operator would
// act on by hand-recreating an object the bundle already restores.
//
// The check below is Resource, not Kind or effect Kind: a PersistentVolume
// is cluster-scoped and by construction never appears in objs; a
// PersistentVolumeClaim is namespaced and, by the same construction,
// always does -- it is exactly what triggered volume.Join to look at it in
// the first place. That is a structural fact about the two kinds, not an
// artifact of which effect Kind happens to name them, so this does not need
// to change if a future analyzer starts producing unknown-data-fate (or any
// other Kind) for some third resource: it excludes precisely the effects
// naming an object the bundle could not have captured, and no others.
//
// One consequence: objects (the report header's total) no longer always
// equals captured + len(excluded). An unbound PVC's unknown-data-fate
// effect still counts toward the header's total (it is a real effect
// volume.Join found) and its object still counts toward captured (the file
// is really there), but it no longer counts here -- so the true
// relationship is objects = captured + excluded + uncertain, where
// uncertain is the number of effects whose object IS captured but whose
// data fate could not be determined. uncertain is not surfaced as its own
// field (the frozen --json "undo" shape in README.md is untouched by this
// fix), but every uncertain effect stays fully visible in the plain-text
// report regardless of the cap, because unknown-data-fate is one of the two
// kinds a cap can never hide (internal/report/report.go's alwaysShown).
func excludedFromEffects(effects []model.Effect) []string {
	var out []string
	for _, e := range effects {
		// A PersistentVolume is cluster-scoped: an effect naming one is
		// genuinely absent from this namespace's bundle no matter which
		// Kind produced it. A PersistentVolumeClaim is namespaced and
		// already captured -- volume.Join only ever looks at a claim
		// because cascade.Enumerate already walked it into objs, and
		// snapshot.Write writes every object in objs regardless of what
		// volume.Join later concludes about it.
		if e.Object.Resource != "persistentvolumes" {
			continue
		}
		switch e.Kind {
		case "destroys-data", "unknown-data-fate":
			out = append(out, fmt.Sprintf("%s/%s: %s", e.Object.Kind, e.Object.Name, e.Explanation))
		case "detaches-data":
			out = append(out, fmt.Sprintf(
				"%s/%s: the data survives (%s), but the PersistentVolume object itself is not captured in this bundle -- it is cluster-scoped, outside the deleted namespace -- and its claimRef must be cleared before the restored PersistentVolumeClaim can rebind to it",
				e.Object.Kind, e.Object.Name, e.Explanation))
		}
	}
	return out
}

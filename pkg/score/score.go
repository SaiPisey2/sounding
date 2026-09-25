// Package score turns an action into a Finding against a live cluster. It
// is the one place the class-composition rule lives --
// Classify(effects, max(verbFloor, volumeClass)) -- so the sounding CLI and
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
func Score(ctx context.Context, c *cluster.Clients, act model.Action, opts Options) (model.Finding, error) {
	// Captured before any request is made, not after enumeration finishes.
	// The report's whole point in stating this is letting the caller judge
	// drift between what was observed and when they act on it; stamping it
	// after the scan's slowest, most-request-heavy phase already ran would
	// under-report exactly the window that matters most on a large cluster.
	scanned := time.Now().UTC()

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

	ns, err := targetNamespace(act, resources)
	if err != nil {
		return model.Finding{}, fmt.Errorf("%w: %v", ErrRefused, err)
	}

	if err := namespaceMustExist(ctx, c, ns); err != nil {
		return model.Finding{}, err
	}

	objs, err := cascade.Enumerate(ctx, c, resources, ns)
	if err != nil {
		return model.Finding{}, fmt.Errorf("%w: enumerating namespace %q: %v", ErrOperational, ns, err)
	}

	effects := destroyEffectsFromObjects(objs)

	volEffects, volClass, err := volume.Join(ctx, c, objs)
	if err != nil {
		return model.Finding{}, fmt.Errorf("%w: joining persistent volumes: %v", ErrOperational, err)
	}
	effects = append(effects, volEffects...)

	finding, err := classify(act, effects, volClass)
	if err != nil {
		// targetNamespace already refuses every verb but "delete" before
		// this point is ever reached, so this is not a real user-facing
		// case today -- it is verbFloor's own defence against a second
		// verb being wired into targetNamespace without anyone adding a
		// matching case here, which would otherwise silently inherit
		// "delete namespace"'s floor for a verb that has never been
		// analysed.
		return model.Finding{}, fmt.Errorf("%w: %v", ErrOperational, err)
	}
	finding.Scanned = scanned
	finding.APICalls = int(*c.Calls)

	if opts.SnapshotDir != "" {
		plan, err := snapshot.Write(ctx, c, opts.SnapshotDir, objs, excludedFromEffects(volEffects))
		if err != nil {
			return model.Finding{}, fmt.Errorf("%w: writing snapshot: %v", ErrOperational, err)
		}
		finding.Undo = plan
		// snapshot.Write issues its own Get calls; the count the report
		// prints must reflect what the scan actually cost, not just the
		// enumeration and volume-join calls made before it.
		finding.APICalls = int(*c.Calls)
	}

	return finding, nil
}

// classify composes the final class from the verb's own floor and the
// worst class the volume join found, then builds the Finding through the
// package's one constructor. It is pulled out of score as its own function
// so the composition rule -- floor := max(verbFloor(act.Verb), volClass) --
// can be exercised directly, without a live cluster, by a test that would
// otherwise have no way to catch a regression here: bypassing verbFloor in
// favour of model.ClassRead compiles cleanly and only shows up as every
// namespace deletion reporting the single most permissive class there is.
func classify(act model.Action, effects []model.Effect, volClass model.Class) (model.Finding, error) {
	floor, err := verbFloor(act.Verb)
	if err != nil {
		return model.Finding{}, err
	}
	if volClass > floor {
		floor = volClass
	}
	return model.NewFinding(act, effects, floor), nil
}

// verbFloor is the worst class this build will ever report for verb,
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
func verbFloor(verb string) (model.Class, error) {
	switch verb {
	case "delete":
		// The objects a namespace deletion destroys ARE restorable from a
		// snapshot even when the data behind some of them is not, which is
		// why this is ClassCompensable and not ClassTerminal -- volume.Join
		// is what raises the floor further, per object, when data
		// specifically cannot come back.
		return model.ClassCompensable, nil
	default:
		return 0, fmt.Errorf("verb %q has no floor defined", verb)
	}
}

// targetNamespace decides which namespace act would tear down, or refuses.
// "delete namespace" is the only verb/resource pair this build understands
// end to end, because cascade.Enumerate always lists an entire namespace's
// contents -- nothing in this codebase yet computes a narrower "what falls
// if only this one object goes" graph.
//
// Namespace itself is deliberately absent from resources: it is a
// cluster-scoped kind, ListableNamespaced only ever reports namespaced
// ones, and no amount of resolving against that list will ever produce it.
// So it is recognised directly, by name, rather than through
// cluster.ResolveResource. Every OTHER resource string -- typed exactly as
// the caller wrote it, since action.ParseCommand no longer guesses a
// plural -- is resolved against live discovery before it is used for
// anything, including before it is named in a refusal, so a caller is
// refused with the resource's real name and, when the string is
// genuinely ambiguous, the real candidates -- never a plural nobody typed
// that happens to match nothing.
func targetNamespace(act model.Action, resources []cluster.Resource) (string, error) {
	if act.Verb != "delete" {
		return "", fmt.Errorf("verb %q has no analyzer", act.Verb)
	}
	if isNamespaceResource(act.Target.Resource) {
		if act.Target.Name == "" {
			return "", fmt.Errorf("delete namespace requires a name")
		}
		return act.Target.Name, nil
	}

	resolved, err := cluster.ResolveResource(resources, act.Target.Resource)
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("delete %s has no analyzer yet -- only delete namespace is supported", resolved.GVR.Resource)
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

// destroyEffectsFromObjects converts a namespace's enumerated objects into
// "destroys" effects, owner-first. It orders objs itself, via
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
func destroyEffectsFromObjects(objs []cascade.Object) []model.Effect {
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
			Explanation: "in the namespace",
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

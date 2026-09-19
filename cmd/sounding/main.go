// Command sounding scores a Kubernetes mutation against live cluster state
// and reports what it would destroy. It executes nothing and holds no
// credential that could -- it needs list and get, nothing else.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/SaiPisey2/sounding/internal/action"
	"github.com/SaiPisey2/sounding/internal/cascade"
	"github.com/SaiPisey2/sounding/internal/cluster"
	"github.com/SaiPisey2/sounding/internal/model"
	"github.com/SaiPisey2/sounding/internal/report"
	"github.com/SaiPisey2/sounding/internal/snapshot"
	"github.com/SaiPisey2/sounding/internal/volume"
)

// errRefused and errOperational are the two non-success outcomes this
// command can produce, and they are kept distinct because they tell a
// caller different things. A refusal means the COMMAND could not be
// scored -- bad input, a resource discovery cannot identify, a cluster
// that cannot be fully enumerated -- and is worth fixing the command for.
// An operational error means sounding itself failed to run -- a bad
// kubeconfig, a network error, a disk write failure -- and is worth fixing
// the environment for. Collapsing the two into one exit code would leave a
// caller's retry logic guessing which kind of failure it is looking at.
var (
	errRefused     = errors.New("refused")
	errOperational = errors.New("operational error")
)

// exitCodeForError is the one place a Go error becomes a process exit
// code. It is tested directly because the exit code -- not the error
// value, which no caller outside this process ever sees -- is the actual
// contract every integration is written against.
func exitCodeForError(err error) int {
	switch {
	case errors.Is(err, errRefused):
		return 2
	case errors.Is(err, errOperational):
		return 1
	default:
		return 1
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

const usage = "usage: sounding score '<command>' [--snapshot DIR] [--kubeconfig PATH] [--json]\n" +
	"       sounding score --stdin [--snapshot DIR] [--kubeconfig PATH] [--json]"

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "refused: no subcommand given")
		fmt.Fprintln(stderr, usage)
		return exitCodeForError(errRefused)
	}
	if args[0] != "score" {
		fmt.Fprintf(stderr, "refused: unknown subcommand %q\n", args[0])
		fmt.Fprintln(stderr, usage)
		return exitCodeForError(errRefused)
	}

	// The command string, when present, is always the first token after
	// "score" -- flags follow it. That lets the positional argument be
	// pulled off by hand before handing the rest to the standard flag
	// package, which would otherwise stop parsing at the first non-flag
	// argument it sees and never reach the flags that follow it.
	rest := args[1:]
	var command string
	haveCommand := len(rest) > 0 && !strings.HasPrefix(rest[0], "-")
	if haveCommand {
		command = rest[0]
		rest = rest[1:]
	}

	fs := flag.NewFlagSet("score", flag.ContinueOnError)
	fs.SetOutput(stderr)
	snapshotDir := fs.String("snapshot", "", "write an undo bundle of every object that would be destroyed to this directory")
	kubeconfig := fs.String("kubeconfig", "", "path to a kubeconfig file; empty means the in-cluster config or the environment default")
	jsonOut := fs.Bool("json", false, "print the finding as JSON instead of the human-readable report")
	useStdin := fs.Bool("stdin", false, "read a JSON-encoded Action from stdin instead of a command string")
	if err := fs.Parse(rest); err != nil {
		return exitCodeForError(errRefused)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "refused: unexpected arguments after flags: %v\n", fs.Args())
		return exitCodeForError(errRefused)
	}
	if haveCommand == *useStdin {
		fmt.Fprintln(stderr, "refused: score needs exactly one of a command string or --stdin")
		return exitCodeForError(errRefused)
	}

	var act model.Action
	var err error
	if *useStdin {
		act, err = action.ReadJSON(stdin)
	} else {
		act, err = action.ParseCommand(command)
	}
	if err != nil {
		refusal := fmt.Errorf("%w: %w", errRefused, err)
		fmt.Fprintf(stderr, "%v\n", refusal)
		return exitCodeForError(refusal)
	}

	finding, err := score(context.Background(), act, *kubeconfig, *snapshotDir)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitCodeForError(err)
	}

	if *jsonOut {
		if err := writeJSON(stdout, finding); err != nil {
			wrapped := fmt.Errorf("%w: encoding finding as json: %v", errOperational, err)
			fmt.Fprintf(stderr, "%v\n", wrapped)
			return exitCodeForError(wrapped)
		}
	} else {
		report.Write(stdout, finding)
	}

	// Only path by which the process exits 0/3/4/5: a completed scan. Every
	// other return above this line is an explicit refusal (2) or
	// operational failure (1), never a class the scan didn't actually
	// reach.
	return finding.Class.ExitCode()
}

// score builds a Finding for act against a live cluster. The class it
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
func score(ctx context.Context, act model.Action, kubeconfig, snapshotDir string) (model.Finding, error) {
	// Captured before any request is made, not after enumeration finishes.
	// The report's whole point in stating this is letting the caller judge
	// drift between what was observed and when they act on it; stamping it
	// after the scan's slowest, most-request-heavy phase already ran would
	// under-report exactly the window that matters most on a large cluster.
	scanned := time.Now().UTC()

	c, err := cluster.New(kubeconfig)
	if err != nil {
		return model.Finding{}, fmt.Errorf("%w: building cluster clients: %v", errOperational, err)
	}

	resources, err := cluster.ListableNamespaced(ctx, c)
	if err != nil {
		if errors.Is(err, cluster.ErrIncompleteDiscovery) {
			// A cluster this tool cannot fully enumerate must never be
			// scored as if it were small. Refusing, rather than reporting
			// whatever partial list came back, is the same rule
			// cluster.ListableNamespaced itself already enforces; this is
			// just where that refusal surfaces as an exit code.
			return model.Finding{}, fmt.Errorf("%w: %w", errRefused, err)
		}
		return model.Finding{}, fmt.Errorf("%w: listing namespaced resources: %v", errOperational, err)
	}

	ns, err := targetNamespace(act, resources)
	if err != nil {
		return model.Finding{}, fmt.Errorf("%w: %v", errRefused, err)
	}

	objs, err := cascade.Enumerate(ctx, c, resources, ns)
	if err != nil {
		return model.Finding{}, fmt.Errorf("%w: enumerating namespace %q: %v", errOperational, ns, err)
	}

	effects := make([]model.Effect, 0, len(objs))
	for _, o := range objs {
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

	volEffects, volClass, err := volume.Join(ctx, c, objs)
	if err != nil {
		return model.Finding{}, fmt.Errorf("%w: joining persistent volumes: %v", errOperational, err)
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
		return model.Finding{}, fmt.Errorf("%w: %v", errOperational, err)
	}
	finding.Scanned = scanned
	finding.APICalls = int(*c.Calls)

	if snapshotDir != "" {
		plan, err := snapshot.Write(ctx, c, snapshotDir, objs, excludedFromEffects(volEffects))
		if err != nil {
			return model.Finding{}, fmt.Errorf("%w: writing snapshot: %v", errOperational, err)
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
// so the composition rule -- floor := max(verbFloor(), volClass) -- can be
// exercised directly, without a live cluster, by a test that would
// otherwise have no way to catch a regression here: dropping verbFloor()
// in favour of model.ClassRead compiles cleanly and only shows up as every
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

func isNamespaceResource(s string) bool {
	switch strings.ToLower(s) {
	case "namespace", "namespaces", "ns":
		return true
	}
	return false
}

// jsonFinding mirrors model.Finding for --json output, with one deliberate
// difference: Class is rendered as its name, not encoded as-is. model.Class
// is an int underneath, and encoding it directly would print e.g. 3 for a
// TERMINAL finding -- which is COMPENSABLE's own exit code. A number that
// silently means a different class depending on which line of the report
// you compare it to is worse than an unsupported flag would have been.
type jsonFinding struct {
	Action   model.Action    `json:"action"`
	Effects  []model.Effect  `json:"effects"`
	Class    string          `json:"class"`
	Undo     *model.UndoPlan `json:"undo,omitempty"`
	Scanned  time.Time       `json:"scanned"`
	APICalls int             `json:"apiCalls"`
}

func writeJSON(w io.Writer, f model.Finding) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(jsonFinding{
		Action:   f.Action,
		Effects:  f.Effects,
		Class:    f.Class.String(),
		Undo:     f.Undo,
		Scanned:  f.Scanned,
		APICalls: f.APICalls,
	})
}

// excludedFromEffects lists, in the words the undo bundle's NOT-RESTORED.txt
// wants, every effect whose data will not come back from the bundle --
// destroyed outright, or simply never understood. An effect this tool
// merely detached (Retain) is not excluded: the data survives, just not
// under this bundle's control.
func excludedFromEffects(effects []model.Effect) []string {
	var out []string
	for _, e := range effects {
		if e.Kind == "destroys-data" || e.Kind == "unknown-data-fate" {
			out = append(out, fmt.Sprintf("%s/%s: %s", e.Object.Kind, e.Object.Name, e.Explanation))
		}
	}
	return out
}

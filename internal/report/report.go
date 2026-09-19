// Package report renders a Finding for the one reader who matters: someone
// deciding whether to let a destructive command run. It writes plain text
// only -- no logic here decides what happened, only how to say it.
package report

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/SaiPisey2/sounding/internal/model"
)

// defaultEffectCap bounds how many effects the plain report lists before
// summarizing the rest. A single namespace deletion against a real cluster
// can enumerate thousands of objects -- one observed run produced 13,200
// effect lines -- and a report that lists all of them buries the signal
// (the class, the counts, the data verdict) at the top under a scroll
// nobody reads, and pushes the one sentence that must never go unread
// ("nothing was executed") wherever the listing happens to end. 20 is
// small enough to fit on one terminal screen alongside the header block
// above it, and large enough to show real texture -- several distinct
// kinds, not just one -- before a reader has to decide whether they want
// the rest.
const defaultEffectCap = 20

// unlimitedEffects disables the cap entirely: used by WriteAll, for a
// reader who has deliberately chosen the full listing (piping to a file or
// a pager, for example).
const unlimitedEffects = -1

// Write renders f for a human about to decide whether to run the command
// that was scored, capping the effect listing at defaultEffectCap. Every
// line answers a question that decision needs answered: what would this
// destroy (the effects table), how sure is that (basis), when was the
// cluster last observed (scanned -- state can drift between the scan and
// the decision, and stating the time is what lets a reader notice the
// drift), what did finding out cost (api calls), and, because a
// blast-radius listing reads exactly like a record of something that
// already happened, that nothing was executed.
func Write(w io.Writer, f model.Finding) {
	write(w, f, defaultEffectCap)
}

// WriteAll renders f with every effect listed, no cap. Use this for the
// --all case: a reader who is piping the report somewhere they control,
// rather than reading it directly in a terminal.
func WriteAll(w io.Writer, f model.Finding) {
	write(w, f, unlimitedEffects)
}

func write(w io.Writer, f model.Finding, cap int) {
	fmt.Fprintf(w, "%s %s/%s\n\n", f.Action.Verb, f.Action.Target.Resource, f.Action.Target.Name)

	basis, matching, total := summarizeBasis(f.Effects)

	header := tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)
	fmt.Fprintf(header, "  class\t%s\n", f.Class)
	if total == 0 {
		// "computed (0/0 effects)" credits evidence that was never
		// gathered -- zero effects is not a basis at all, it is the
		// absence of one, and the two must not read the same.
		fmt.Fprintf(header, "  basis\tno effects observed\n")
	} else {
		fmt.Fprintf(header, "  basis\t%s (%d/%d effects)\n", basis, matching, total)
	}
	fmt.Fprintf(header, "  objects\t%d across %d kinds\n", total, len(distinctKinds(f.Effects)))
	fmt.Fprintf(header, "  scanned\t%s, %d api calls\n", f.Scanned.UTC().Format(time.RFC3339), f.APICalls)
	header.Flush()
	fmt.Fprintln(w)

	// Printed here -- immediately after the header, before the effect
	// listing -- rather than at the end. On a real cluster the listing
	// below can run to thousands of lines, and a sentence that exists
	// specifically so a blast-radius listing is never mistaken for a
	// record of something that already happened is not a safety line if
	// it only appears after that scroll. Printing it first means it is
	// seen whether the listing is capped, uncapped, or piped somewhere
	// nobody watches live.
	fmt.Fprintln(w, "  Nothing was executed. sounding holds no credential that could.")
	fmt.Fprintln(w)

	writeEffects(w, f.Effects, cap)

	// Undo is populated only when --snapshot ran; it is never consulted by
	// Classify, so its presence here changes what the report SAYS, never
	// what it CONCLUDES.
	if f.Undo != nil {
		fmt.Fprintf(w, "  undo       %d objects captured under %s\n", f.Undo.Objects, f.Undo.Dir)
		if len(f.Undo.Excluded) > 0 {
			fmt.Fprintf(w, "             %d not restorable by the bundle -- see NOT-RESTORED.txt\n", len(f.Undo.Excluded))
		}
		fmt.Fprintln(w)
	}
}

// alwaysShown and deprioritizedKinds below are keyed on two DIFFERENT
// fields that both happen to be called Kind, two lines apart -- easy to
// swap by accident and get something that still compiles. alwaysShown
// tests e.Kind, the model.Effect's own kind (what HAPPENED: "destroys",
// "destroys-data", ...). deprioritizedKinds tests e.Object.Kind, the
// Kubernetes kind of the thing the effect is ABOUT ("Pod", "Event", ...).
// Neither is more "correct" than the other in isolation; each is right
// for what it answers, and only the field name distinguishes them.
//
// alwaysShown holds the effect kinds a cap must never hide, regardless of
// their position in the list:
//
//   - "destroys-data" -- data is gone and unrecoverable. Rare even in a
//     huge namespace, and the single fact most likely to change what a
//     reader decides to do.
//   - "unknown-data-fate" -- sounding does NOT know whether the data
//     survives (an unrecognised reclaim policy, or an unbound claim).
//     Hiding this one is worse than hiding a known-destructive effect:
//     summarising a destroys-data effect at least tells the reader there
//     is a known loss to look into; summarising an unknown-data-fate one
//     hides the fact that there is anything to look into at all.
//
// "detaches-data" is deliberately NOT exempt: it means the data survives,
// which is the one data-related outcome where summarising it behind
// "...and N more" costs the reader nothing.
var alwaysShown = map[string]bool{
	"destroys-data":     true,
	"unknown-data-fate": true,
}

// deprioritizedKinds holds object kinds that must not compete for the
// cap's scarce visible slots while anything else is available. Events are
// enumerated exactly like any other namespaced resource, but a namespace
// under ordinary Pod-lifecycle churn can carry dozens of them, while the
// thing an operator is actually asking "what will this destroy" about --
// a Deployment, a ReplicaSet, a Pod -- is comparatively rare. Left to a
// purely positional cap, Events crowd every workload out: one run against
// a real cluster filled all 20 visible slots with 15 Events, 2 PVCs, 2
// ConfigMaps and a PersistentVolume -- no Deployment, no ReplicaSet, no
// Pod, the report's twenty most valuable lines spent on its least valuable
// objects.
//
// This is deliberately narrow. EndpointSlice and ControllerRevision were
// considered and left out: both CAN accumulate, but neither combines "very
// high count relative to workloads" with "near-zero relevance to the
// question this report answers" as unambiguously as Event does, and
// nothing has shown them causing the same crowding on a real cluster.
var deprioritizedKinds = map[string]bool{
	"Event": true,
}

// writeEffects lists effects, honouring cap (unlimitedEffects for no cap).
// It renders in exactly the order effects arrives -- cascade.Order's
// owner-first sequence, restored in the previous fix round, must survive
// selecting which effects to show. selectShown decides the WHICH; this
// function only decides whether each already-decided index gets printed,
// walking the slice once in its original order.
func writeEffects(w io.Writer, effects []model.Effect, cap int) {
	if len(effects) == 0 {
		return
	}

	shown := selectShown(effects, cap)

	body := tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)
	count := 0
	for i, e := range effects {
		if shown[i] {
			fmt.Fprintf(body, "  %s\t%s/%s\t%s\n", e.Kind, e.Object.Kind, e.Object.Name, e.Explanation)
			count++
		}
	}
	body.Flush()

	// The count here must be exact, not approximate: a tool whose entire
	// job is exact counts cannot round off the one number describing what
	// its own report chose not to show.
	if hidden := len(effects) - count; hidden > 0 {
		fmt.Fprintf(w, "  ... and %d more (use --all to list every effect)\n", hidden)
	}
	fmt.Fprintln(w)
}

// selectShown decides WHICH effects fill the cap; it never reorders
// anything -- the caller renders the original slice in its original
// order, checking this result index by index. Three tiers, in priority
// order:
//
//  1. alwaysShown effects (destroys-data, unknown-data-fate) are chosen
//     unconditionally, exactly as in the previous two fix rounds, and do
//     NOT draw against the ordinary budget below -- each one is ADDITIVE
//     to cap, not a substitute for one of its slots, regardless of where
//     in the list it sits. A cap of 20 with one forced effect anywhere in
//     the list shows 21, not 20; the total shown is only ever bounded by
//     cap when no forced effect is present. This is a deliberate choice,
//     not an oversight: a cap that could shrink to make room for a forced
//     effect would sometimes hide an ORDINARY object to make space for a
//     data-fate one, which is a worse trade than the listing simply
//     running one line longer. (An earlier, purely positional
//     implementation made this same choice by accident, but only for a
//     forced effect that fell past the first cap positions; one inside
//     them changed nothing, because it was already going to be shown
//     either way. This implementation makes the choice uniform regardless
//     of position, which is why the tests below cover both cases.)
//  2. Ordinary effects -- anything not in tier 1 or 3 -- claim the
//     ordinary budget (cap slots) first, in their original order.
//  3. Deprioritized effects (Event) only receive whatever budget tier 2
//     did not use, also in their original order.
//
// Splitting selection into three single passes over the same slice, each
// keyed off what the previous pass already claimed, is what keeps a
// workload from ever losing a slot to an Event while an ordinary slot
// still exists, without touching the render order at all.
func selectShown(effects []model.Effect, cap int) []bool {
	shown := make([]bool, len(effects))
	if cap < 0 {
		for i := range shown {
			shown[i] = true
		}
		return shown
	}

	for i, e := range effects {
		if alwaysShown[e.Kind] {
			shown[i] = true
		}
	}

	budget := cap
	for i, e := range effects {
		if shown[i] || budget <= 0 || deprioritizedKinds[e.Object.Kind] {
			continue
		}
		shown[i] = true
		budget--
	}
	for i, e := range effects {
		if shown[i] || budget <= 0 || !deprioritizedKinds[e.Object.Kind] {
			continue
		}
		shown[i] = true
		budget--
	}

	return shown
}

// summarizeBasis names the least reliable basis present -- unknown is worse
// than declared is worse than computed, the same ordering basisFloor in
// internal/model uses to set a class floor -- and counts how many effects
// share it. A reader needs both halves: which kind of evidence the weakest
// part of this finding rests on, and how much of the finding that actually
// is. When every effect shares one basis (the common case today; nothing
// yet supplies BasisDeclared) this reads as, e.g., "computed (47/47
// effects)".
func summarizeBasis(effects []model.Effect) (label string, matching, total int) {
	total = len(effects)
	worst := model.BasisComputed
	worstRank := -1
	for _, e := range effects {
		if r := basisRank(e.Basis); r > worstRank {
			worstRank = r
			worst = e.Basis
		}
	}
	if worstRank < 0 {
		return string(model.BasisComputed), 0, 0
	}
	for _, e := range effects {
		if e.Basis == worst {
			matching++
		}
	}
	return string(worst), matching, total
}

// distinctKinds returns the set of object kinds present across effects, so
// the report can say "N across K kinds" instead of leaving a reader to
// count a table by eye -- and so "zero" is stated as 0 objects across 0
// kinds rather than by an empty section nobody can tell apart from a
// report that simply forgot to render one.
func distinctKinds(effects []model.Effect) map[string]bool {
	kinds := make(map[string]bool)
	for _, e := range effects {
		kinds[e.Object.Kind] = true
	}
	return kinds
}

func basisRank(b model.Basis) int {
	switch b {
	case model.BasisUnknown:
		return 2
	case model.BasisDeclared:
		return 1
	default: // model.BasisComputed
		return 0
	}
}

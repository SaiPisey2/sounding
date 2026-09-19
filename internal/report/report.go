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

// writeEffects lists effects, honouring cap (unlimitedEffects for no cap).
// An effect whose kind is in alwaysShown is shown regardless of its
// position and is counted against the cap like anything else that is
// shown, so the "N more" count stays exact rather than silently drifting
// once a forced inclusion is involved.
func writeEffects(w io.Writer, effects []model.Effect, cap int) {
	if len(effects) == 0 {
		return
	}

	body := tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)
	shown := 0
	for i, e := range effects {
		if cap < 0 || i < cap || alwaysShown[e.Kind] {
			fmt.Fprintf(body, "  %s\t%s/%s\t%s\n", e.Kind, e.Object.Kind, e.Object.Name, e.Explanation)
			shown++
		}
	}
	body.Flush()

	// The count here must be exact, not approximate: a tool whose entire
	// job is exact counts cannot round off the one number describing what
	// its own report chose not to show.
	if hidden := len(effects) - shown; hidden > 0 {
		fmt.Fprintf(w, "  ... and %d more (use --all to list every effect)\n", hidden)
	}
	fmt.Fprintln(w)
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

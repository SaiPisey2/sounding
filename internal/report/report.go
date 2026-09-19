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

// Write renders f for a human about to decide whether to run the command
// that was scored. Every line answers a question that decision needs
// answered: what would this destroy (the effects table), how sure is that
// (basis), when was the cluster last observed (scanned -- state can drift
// between the scan and the decision, and stating the time is what lets a
// reader notice the drift), what did finding out cost (api calls), and,
// because a blast-radius listing reads exactly like a record of something
// that already happened, that nothing was executed.
func Write(w io.Writer, f model.Finding) {
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

	if len(f.Effects) > 0 {
		body := tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)
		for _, e := range f.Effects {
			fmt.Fprintf(body, "  %s\t%s/%s\t%s\n", e.Kind, e.Object.Kind, e.Object.Name, e.Explanation)
		}
		body.Flush()
		fmt.Fprintln(w)
	}

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

	// This sentence is the one that must never be omitted or diluted: a
	// listing of what an action WOULD destroy is indistinguishable in
	// format from a log of what already was destroyed. Only this line
	// tells the two apart.
	fmt.Fprintln(w, "  Nothing was executed. sounding holds no credential that could.")
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

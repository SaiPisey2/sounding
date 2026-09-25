package report

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SaiPisey2/sounding/pkg/model"
)

// manyEffects builds n "destroys" effects, all BasisComputed, named
// obj-0..obj-n-1 in order -- enough to exercise the report's cap without
// depending on a real cluster.
func manyEffects(n int) []model.Effect {
	effects := make([]model.Effect, n)
	for i := range effects {
		effects[i] = model.Effect{
			Kind: "destroys", Basis: model.BasisComputed,
			Object:      model.Target{Kind: "Pod", Name: fmt.Sprintf("obj-%d", i)},
			Explanation: "in the namespace",
		}
	}
	return effects
}

func finding() model.Finding {
	return model.Finding{
		Action: model.Action{Verb: "delete", Target: model.Target{Resource: "namespaces", Name: "prod-payments"}},
		Effects: []model.Effect{
			{Kind: "destroys", Basis: model.BasisComputed, Object: model.Target{Kind: "Pod", Name: "api-1"}, Explanation: "in the namespace"},
			{Kind: "destroys-data", Basis: model.BasisComputed, Object: model.Target{Kind: "PersistentVolume", Name: "pv-1"}, Explanation: "reclaimPolicy=Delete"},
		},
		Class: model.ClassTerminal, Scanned: time.Unix(0, 0).UTC(), APICalls: 61,
	}
}

func TestReportStatesTheClassAndTheBasis(t *testing.T) {
	var b bytes.Buffer
	Write(&b, finding())
	s := b.String()
	for _, want := range []string{"TERMINAL", "computed", "pv-1", "reclaimPolicy=Delete"} {
		if !strings.Contains(s, want) {
			t.Errorf("report does not mention %q:\n%s", want, s)
		}
	}
}

// The scan is a point-in-time observation and the caller acts later. Saying
// when it was taken is what lets them notice the window.
func TestReportStatesWhenItLooked(t *testing.T) {
	var b bytes.Buffer
	Write(&b, finding())
	if !strings.Contains(b.String(), "1970") {
		t.Error("report must state the scan time -- state can change before the caller acts")
	}
}

func TestReportStatesItsCost(t *testing.T) {
	var b bytes.Buffer
	Write(&b, finding())
	if !strings.Contains(b.String(), "61") {
		t.Error("report must state how many api calls the scan made")
	}
}

// Nothing is executed, ever. The report says so, so that nobody reads a
// blast-radius listing as a record of something that already happened.
func TestReportSaysNothingWasExecuted(t *testing.T) {
	var b bytes.Buffer
	Write(&b, finding())
	s := strings.ToLower(b.String())
	if !strings.Contains(s, "nothing was executed") {
		t.Error("report must state that it executed nothing")
	}
}

// The basis line is the one that stops a guess wearing a measurement's
// credibility: it must name the LEAST reliable basis present, not the most
// common or the most reassuring one, and say how many effects actually
// share it. Three computed effects and one whose data-fate is unknown must
// never be summarized as "computed" -- that would print full confidence
// for a finding that is one quarter guesswork.
func TestReportNamesTheWeakestBasisAndCountsIt(t *testing.T) {
	f := finding()
	f.Effects = []model.Effect{
		{Kind: "destroys", Basis: model.BasisComputed, Object: model.Target{Kind: "Pod", Name: "a"}, Explanation: "in the namespace"},
		{Kind: "destroys", Basis: model.BasisComputed, Object: model.Target{Kind: "Pod", Name: "b"}, Explanation: "in the namespace"},
		{Kind: "destroys", Basis: model.BasisComputed, Object: model.Target{Kind: "Pod", Name: "c"}, Explanation: "in the namespace"},
		{Kind: "unknown-data-fate", Basis: model.BasisUnknown, Object: model.Target{Kind: "PersistentVolumeClaim", Name: "d"}, Explanation: "no spec.volumeName"},
	}

	var b bytes.Buffer
	Write(&b, f)
	if !strings.Contains(b.String(), "unknown (1/4 effects)") {
		t.Errorf("report must name the weakest basis and how many effects share it, want \"unknown (1/4 effects)\":\n%s", b.String())
	}
}

// A real cluster can produce thousands of effects from a single namespace
// deletion (one observed run produced 13,200 lines). The default report
// must cap the listing and say EXACTLY how many effects were not shown --
// an approximate count on a tool whose entire job is exact counts would
// undermine everything else it says.
func TestReportCapsTheEffectListingAndCountsTheRemainderExactly(t *testing.T) {
	f := finding()
	f.Effects = manyEffects(25)

	var b bytes.Buffer
	Write(&b, f)
	s := b.String()
	if !strings.Contains(s, "... and 5 more (use --all to list every effect)") {
		t.Errorf("report must cap the listing and name the exact remainder (25 effects, cap 20 -> 5 more):\n%s", s)
	}
	if strings.Contains(s, "obj-24") {
		t.Errorf("the 25th effect should have been capped, not shown:\n%s", s)
	}
}

// --all is the escape hatch for a reader who has deliberately chosen the
// full listing -- piping to a file or a pager, say. It must show
// everything, with no "more" line left over.
func TestWriteAllShowsEveryEffect(t *testing.T) {
	f := finding()
	f.Effects = manyEffects(25)

	var b bytes.Buffer
	WriteAll(&b, f)
	s := b.String()
	if strings.Contains(s, "more (use --all") {
		t.Errorf("--all must not itself be capped:\n%s", s)
	}
	if !strings.Contains(s, "obj-0") || !strings.Contains(s, "obj-24") {
		t.Errorf("--all must show every effect, first and last:\n%s", s)
	}
}

// A destroys-data effect decides whether data is recoverable at all. It
// must never be among the effects a cap hides, even when its position in
// the list is well past the cap.
func TestCappedListingNeverHidesADestroysDataEffect(t *testing.T) {
	f := finding()
	effects := manyEffects(24)
	effects = append(effects, model.Effect{
		Kind: "destroys-data", Basis: model.BasisComputed,
		Object:      model.Target{Kind: "PersistentVolume", Name: "pv-buried"},
		Explanation: "reclaimPolicy=Delete",
	})
	f.Effects = effects // 25 effects; the destroys-data one is at index 24, past the cap of 20

	var b bytes.Buffer
	Write(&b, f)
	s := b.String()
	if !strings.Contains(s, "PersistentVolume/pv-buried") {
		t.Errorf("a destroys-data effect past the cap must still be shown:\n%s", s)
	}
	// The forced destroys-data effect is ADDITIONAL to the 20-effect cap,
	// not a substitute for one of its slots: 20 ordinary + 1 forced = 21
	// shown, so 4 of the 25 total remain hidden.
	if !strings.Contains(s, "... and 4 more (use --all to list every effect)") {
		t.Errorf("a forced destroys-data effect must be additional to the cap, and the remainder must reflect that exactly:\n%s", s)
	}
}

// unknown-data-fate means sounding does NOT know whether the data behind
// an object survives -- an unrecognised reclaim policy, or an unbound
// claim. Hiding this behind "...and N more" is worse than hiding a known
// destroys-data effect: the reader cannot even know there is something to
// look into. It must never be among the effects a cap hides, even when its
// position in the list is well past the cap.
func TestCappedListingNeverHidesAnUnknownDataFateEffect(t *testing.T) {
	f := finding()
	effects := manyEffects(24)
	effects = append(effects, model.Effect{
		Kind: "unknown-data-fate", Basis: model.BasisUnknown,
		Object:      model.Target{Kind: "PersistentVolumeClaim", Name: "pvc-buried"},
		Explanation: "no spec.volumeName",
	})
	f.Effects = effects // 25 effects; the unknown-data-fate one is at index 24, past the cap of 20

	var b bytes.Buffer
	Write(&b, f)
	s := b.String()
	if !strings.Contains(s, "PersistentVolumeClaim/pvc-buried") {
		t.Errorf("an unknown-data-fate effect past the cap must still be shown:\n%s", s)
	}
	// The forced unknown-data-fate effect is ADDITIONAL to the 20-effect
	// cap, not a substitute for one of its slots: 20 ordinary + 1 forced =
	// 21 shown, so 4 of the 25 total remain hidden.
	if !strings.Contains(s, "... and 4 more (use --all to list every effect)") {
		t.Errorf("a forced unknown-data-fate effect must be additional to the cap, and the remainder must reflect that exactly:\n%s", s)
	}
}

// detaches-data means the data survives -- the one data-related outcome
// where summarising it behind the cap costs the reader nothing. It must
// stay under the ordinary cap, not be forced through like the other two.
func TestCappedListingStillHidesADetachesDataEffectPastTheCap(t *testing.T) {
	f := finding()
	effects := manyEffects(24)
	effects = append(effects, model.Effect{
		Kind: "detaches-data", Basis: model.BasisComputed,
		Object:      model.Target{Kind: "PersistentVolume", Name: "pv-retained"},
		Explanation: "reclaimPolicy=Retain",
	})
	f.Effects = effects

	var b bytes.Buffer
	Write(&b, f)
	s := b.String()
	if strings.Contains(s, "pv-retained") {
		t.Errorf("a detaches-data effect past the cap should be summarised, not forced through:\n%s", s)
	}
	if !strings.Contains(s, "... and 5 more (use --all to list every effect)") {
		t.Errorf("the ordinary cap must still apply to detaches-data:\n%s", s)
	}
}

// A forced (alwaysShown) effect is additive to the cap wherever it sits in
// the list, not only when it falls past the first cap positions. The two
// tests above only ever place their forced effect at index 24, well past
// the cap of 20, so they cannot tell "additive everywhere" apart from "only
// additive when past the cap" -- exactly the distinction an earlier,
// purely positional implementation got wrong for a forced effect that fell
// INSIDE the first cap positions (it changed nothing there, since that
// effect was already going to be shown either way). This places the forced
// effect at index 5 and asserts the identical arithmetic as the past-the-
// cap fixtures: 20 ordinary + 1 forced = 21 shown, 4 hidden.
func TestForcedEffectIsAdditiveWhereverItSitsInTheList(t *testing.T) {
	for _, kind := range []string{"destroys-data", "unknown-data-fate"} {
		t.Run(kind, func(t *testing.T) {
			f := finding()
			var effects []model.Effect
			effects = append(effects, manyEffects(5)...)
			effects = append(effects, model.Effect{
				Kind: kind, Basis: model.BasisComputed,
				Object:      model.Target{Kind: "PersistentVolume", Name: "pv-inside-the-cap"},
				Explanation: "forced",
			})
			effects = append(effects, manyEffects(19)...)
			f.Effects = effects // 25 total: 5 ordinary + 1 forced (index 5, well inside cap 20) + 19 ordinary

			var b bytes.Buffer
			Write(&b, f)
			s := b.String()
			if !strings.Contains(s, "PersistentVolume/pv-inside-the-cap") {
				t.Errorf("a forced %s effect inside the cap must still be shown:\n%s", kind, s)
			}
			if !strings.Contains(s, "... and 4 more (use --all to list every effect)") {
				t.Errorf("a forced %s effect inside the cap must be additive, exactly like one past the cap:\n%s", kind, s)
			}
		})
	}
}

// eventsAndWorkloads builds the fixture shape that broke on a real
// cluster: a run of Events -- ordinary Pod-lifecycle churn, unrelated to
// the specific thing being scored -- followed by the actual workload
// chain an operator cares about. numEvents is deliberately larger than
// defaultEffectCap, so a purely positional cap would fill every visible
// slot with Events alone and hide the workloads entirely.
func eventsAndWorkloads(numEvents int) []model.Effect {
	effects := make([]model.Effect, 0, numEvents+3)
	for i := 0; i < numEvents; i++ {
		effects = append(effects, model.Effect{
			Kind: "destroys", Basis: model.BasisComputed,
			Object:      model.Target{Kind: "Event", Name: fmt.Sprintf("evt-%d", i)},
			Explanation: "in the namespace",
		})
	}
	return append(effects,
		model.Effect{Kind: "destroys", Basis: model.BasisComputed, Object: model.Target{Kind: "Deployment", Name: "api"}, Explanation: "in the namespace"},
		model.Effect{Kind: "destroys", Basis: model.BasisComputed, Object: model.Target{Kind: "ReplicaSet", Name: "api-1"}, Explanation: "in the namespace"},
		model.Effect{Kind: "destroys", Basis: model.BasisComputed, Object: model.Target{Kind: "Pod", Name: "api-1-x"}, Explanation: "in the namespace"},
	)
}

// This is the bug a real cluster surfaced after round 4's ordering fix:
// with 25 Events ahead of 3 workloads in cascade order, a purely
// positional cap fills all 20 visible slots with Events and hides every
// workload -- the exact "15 Event, 2 PVC, 2 ConfigMap, 1 PV, no
// Deployment/ReplicaSet/Pod" shape observed against the fixture cluster.
func TestVisibleListingDeprioritizesEventsInFavourOfWorkloads(t *testing.T) {
	f := finding()
	f.Effects = eventsAndWorkloads(25) // 28 effects total, cap 20

	var b bytes.Buffer
	Write(&b, f)
	s := b.String()
	for _, want := range []string{"Deployment/api", "ReplicaSet/api-1", "Pod/api-1-x"} {
		if !strings.Contains(s, want) {
			t.Errorf("workload %q must be visible even with many Events present:\n%s", want, s)
		}
	}
}

// Deprioritizing Events changes WHICH effects fill the cap, not how many.
// With 25 Events + 3 workloads = 28 effects and a cap of 20, the 3
// workloads claim 3 of the budget and the remaining 17 slots go to the
// first 17 Events, leaving 8 Events hidden -- the remainder count must
// still be exact under this selection.
func TestRemainderCountIsExactWhenEventsAreDeprioritized(t *testing.T) {
	f := finding()
	f.Effects = eventsAndWorkloads(25)

	var b bytes.Buffer
	Write(&b, f)
	s := b.String()
	if !strings.Contains(s, "... and 8 more (use --all to list every effect)") {
		t.Errorf("remainder count must be exact once Events are deprioritized (25 events + 3 workloads, cap 20, 3 workloads + 17 events shown -> 8 hidden):\n%s", s)
	}
}

// Nothing about an Event changes under --all: it is still enumerated,
// still counted, and still shown in full -- only its claim on the
// default-cap listing is deprioritized.
func TestWriteAllStillShowsEveryEvent(t *testing.T) {
	f := finding()
	f.Effects = eventsAndWorkloads(25)

	var b bytes.Buffer
	WriteAll(&b, f)
	s := b.String()
	if strings.Contains(s, "more (use --all") {
		t.Errorf("--all must not itself be capped:\n%s", s)
	}
	for i := 0; i < 25; i++ {
		want := fmt.Sprintf("Event/evt-%d", i)
		if !strings.Contains(s, want) {
			t.Errorf("--all must show every Event, including %q:\n%s", want, s)
		}
	}
}

// "Nothing was executed" exists so a blast-radius listing is never mistaken
// for a record of something that already happened. On a real cluster the
// listing can run to thousands of lines, so the sentence is worthless
// unless it comes before that listing, not after it.
func TestNothingWasExecutedPrecedesTheEffectListing(t *testing.T) {
	f := finding()
	f.Effects = manyEffects(5)

	var b bytes.Buffer
	Write(&b, f)
	s := b.String()

	notice := strings.Index(s, "Nothing was executed")
	firstEffect := strings.Index(s, "obj-0")
	if notice < 0 {
		t.Fatalf("report does not contain the not-executed notice:\n%s", s)
	}
	if firstEffect < 0 {
		t.Fatalf("report does not contain the first effect:\n%s", s)
	}
	if notice > firstEffect {
		t.Errorf("\"Nothing was executed\" must appear before the effect listing, not after:\n%s", s)
	}
}

// A finding with no effects at all has found nothing -- the report must say
// that plainly rather than printing "basis computed (0/0 effects)", which
// credits evidence that was never gathered.
func TestReportStatesPlainlyWhenNoEffectsWereFound(t *testing.T) {
	f := finding()
	f.Effects = nil
	f.Class = model.ClassRead

	var b bytes.Buffer
	Write(&b, f)
	s := b.String()
	if !strings.Contains(s, "no effects observed") {
		t.Errorf("report must state plainly that nothing was found:\n%s", s)
	}
	if strings.Contains(s, "computed (0/0") {
		t.Errorf("report must not credit evidence that does not exist:\n%s", s)
	}
	if !strings.Contains(s, "0 across 0 kinds") {
		t.Errorf("report must state the object count positively as zero:\n%s", s)
	}
}

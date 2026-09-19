package report

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SaiPisey2/sounding/internal/model"
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
	// 20 capped + 1 forced destroys-data = 21 shown, so 4 remain hidden.
	if !strings.Contains(s, "... and 4 more (use --all to list every effect)") {
		t.Errorf("the forced destroys-data effect must count against the cap so the remainder stays exact:\n%s", s)
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

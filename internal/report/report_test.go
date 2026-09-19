package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/SaiPisey2/sounding/internal/model"
)

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

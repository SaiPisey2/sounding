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

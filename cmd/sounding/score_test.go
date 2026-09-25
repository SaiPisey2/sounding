package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SaiPisey2/sounding/pkg/model"
)

// Encoding model.Finding directly would print Class as a bare int -- 3 for
// TERMINAL, which happens to be COMPENSABLE's own exit code. This is the
// contract test for --json: it must produce valid JSON, and the class must
// read as its name, with the effects and their bases present so a
// machine reader has the same evidence a human reading the plain report
// does.
func TestJSONOutputRendersTheClassAsItsNameNotACollidingNumber(t *testing.T) {
	f := model.Finding{
		Action: model.Action{Verb: "delete", Target: model.Target{Resource: "namespaces", Name: "prod-payments"}},
		Effects: []model.Effect{
			{Kind: "destroys", Basis: model.BasisComputed, Object: model.Target{Kind: "Pod", Name: "api-1"}, Explanation: "in the namespace"},
			{Kind: "destroys-data", Basis: model.BasisComputed, Object: model.Target{Kind: "PersistentVolume", Name: "pv-1"}, Explanation: "reclaimPolicy=Delete"},
		},
		Class: model.ClassTerminal, Scanned: time.Unix(0, 0).UTC(), APICalls: 61,
	}

	var b bytes.Buffer
	if err := writeJSON(&b, f); err != nil {
		t.Fatalf("writeJSON errored: %v", err)
	}

	var decoded struct {
		Class   string `json:"class"`
		Effects []struct {
			Basis string `json:"basis"`
		} `json:"effects"`
	}
	if err := json.Unmarshal(b.Bytes(), &decoded); err != nil {
		t.Fatalf("--json output does not parse as JSON: %v\n%s", err, b.String())
	}
	if decoded.Class != "TERMINAL" {
		t.Errorf("class = %q, want \"TERMINAL\" (a bare 3 collides with COMPENSABLE's exit code)", decoded.Class)
	}
	if len(decoded.Effects) != 2 {
		t.Fatalf("got %d effects in the json output, want 2", len(decoded.Effects))
	}
	for _, e := range decoded.Effects {
		if e.Basis != "computed" {
			t.Errorf("effect basis = %q, want \"computed\"", e.Basis)
		}
	}
}

// Before json tags existed on model.Action, Target, Effect and UndoPlan,
// one --json document mixed casing conventions: the top level read
// action/effects/apiCalls (from jsonFinding's own tags) while the nested
// action and effects read Verb/Group/Kind/Basis (the untagged Go field
// names underneath). v1.0.0 freezes this wire format, so every field at
// every nesting level must use the same lowercase convention.
func TestJSONOutputIsConsistentlyCasedAtEveryNestingLevel(t *testing.T) {
	f := model.Finding{
		Action: model.Action{
			Verb:   "delete",
			Target: model.Target{Group: "", Version: "v1", Resource: "namespaces", Kind: "Namespace", Name: "prod-payments"},
		},
		Effects: []model.Effect{
			{
				Kind:  "destroys-data",
				Basis: model.BasisComputed,
				Object: model.Target{
					Group: "", Version: "v1", Resource: "persistentvolumes", Kind: "PersistentVolume", Name: "pv-1",
				},
				Explanation: "reclaimPolicy=Delete",
			},
		},
		Class:    model.ClassTerminal,
		Undo:     &model.UndoPlan{Dir: "/tmp/undo", Objects: 1, Excluded: []string{"PersistentVolume/pv-1: reclaimPolicy=Delete"}},
		Scanned:  time.Unix(0, 0).UTC(),
		APICalls: 61,
	}

	var b bytes.Buffer
	if err := writeJSON(&b, f); err != nil {
		t.Fatalf("writeJSON errored: %v", err)
	}
	s := b.String()

	// Every one of these must appear as a JSON key: the nested Action,
	// Target (twice: action.target and effects[].object) and Effect fields
	// that had no tag at all before this fix, alongside the top-level
	// jsonFinding fields that already did.
	for _, key := range []string{
		`"action"`, `"verb"`, `"target"`, `"group"`, `"version"`, `"resource"`, `"kind"`, `"name"`,
		`"effects"`, `"object"`, `"basis"`, `"explanation"`,
		`"class"`, `"undo"`, `"dir"`, `"objects"`, `"excluded"`, `"scanned"`, `"apiCalls"`,
	} {
		if !strings.Contains(s, key) {
			t.Errorf("json output missing expected key %s:\n%s", key, s)
		}
	}
	// And none of the untagged Go field names this fix removed should
	// survive as capitalised JSON keys.
	for _, stale := range []string{`"Verb"`, `"Group"`, `"Resource"`, `"Kind"`, `"Basis"`, `"Explanation"`, `"Dir"`, `"Objects"`, `"Excluded"`} {
		if strings.Contains(s, stale) {
			t.Errorf("json output still has a capitalised, untagged field name %s:\n%s", stale, s)
		}
	}
}

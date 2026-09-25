package model

import "time"

type Basis string

const (
	BasisComputed Basis = "computed"
	BasisDeclared Basis = "declared"
	BasisUnknown  Basis = "unknown"
)

type Class int

const (
	ClassRead Class = iota
	ClassReversible
	ClassCompensable
	ClassTerminal
	ClassAuthority
)

// Target's fields, and the other three types below, carry explicit json
// tags because they are v1.0.0's wire format on two sides at once: --json
// encodes them on the way out, and --stdin's action.ReadJSON decodes an
// Action from exactly this shape on the way in. Before this, only the
// command's own jsonFinding wrapper (cmd/sounding/main.go) had tags --
// these four types had none, so their Go field names leaked into the
// output as-is and one document mixed "action"/"effects"/"apiCalls" at the
// top level with "Verb"/"Group"/"Kind"/"Basis" one level down. Freezing the
// tags here is what keeps that shape from drifting the next time a field on
// any of these four is renamed for an unrelated reason.
type Target struct {
	Group     string `json:"group"`
	Version   string `json:"version"`
	Resource  string `json:"resource"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type Action struct {
	Verb    string         `json:"verb"`
	Target  Target         `json:"target"`
	Payload map[string]any `json:"payload,omitempty"`
}

type Effect struct {
	Kind        string `json:"kind"`
	Object      Target `json:"object"`
	Basis       Basis  `json:"basis"`
	Explanation string `json:"explanation"`
}

type UndoPlan struct {
	Dir      string   `json:"dir"`
	Objects  int      `json:"objects"`
	Excluded []string `json:"excluded,omitempty"`
}

// Finding records an action and its effects. Must be built with NewFinding, which
// is the only supported way to build one. Class's zero value is ClassRead (the most
// permissive class there is), so a Finding assembled by struct literal and never
// classified would report the safest possible answer — which is the exact failure
// this package exists to stop.
type Finding struct {
	Action   Action
	Effects  []Effect
	Class    Class
	Undo     *UndoPlan
	Scanned  time.Time
	APICalls int
}

// NewFinding is the only supported way to build a Finding, because Class's
// zero value is ClassRead -- the most permissive class there is. A Finding
// assembled by struct literal and never classified would report the safest
// possible answer, which is the exact failure this package exists to stop.
//
// It applies no verb floor: floor is whatever the caller passes, and the
// effects' bases only ever raise it. Passing ClassRead for a delete yields
// a finding as safe as its weakest evidence, however destructive the verb.
// A caller scoring an action should use score.Score, which supplies the
// verb's floor; NewFinding is for callers that have already decided one.
func NewFinding(a Action, effects []Effect, floor Class) Finding {
	return Finding{
		Action:  a,
		Effects: effects,
		Class:   Classify(effects, floor),
	}
}

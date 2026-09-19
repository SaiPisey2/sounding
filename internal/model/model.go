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

type Target struct {
	Group     string
	Version   string
	Resource  string
	Kind      string
	Namespace string
	Name      string
}

type Action struct {
	Verb    string
	Target  Target
	Payload map[string]any
}

type Effect struct {
	Kind        string
	Object      Target
	Basis       Basis
	Explanation string
}

type UndoPlan struct {
	Dir      string
	Objects  int
	Excluded []string
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
func NewFinding(a Action, effects []Effect, floor Class) Finding {
	return Finding{
		Action:  a,
		Effects: effects,
		Class:   Classify(effects, floor),
	}
}

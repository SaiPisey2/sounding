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

type Finding struct {
	Action   Action
	Effects  []Effect
	Class    Class
	Undo     *UndoPlan
	Scanned  time.Time
	APICalls int
}

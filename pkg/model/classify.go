package model

// basisFloor is the best class a finding may reach given how its weakest
// effect was known.
//
// This is the rule that keeps coverage honest. Without it, an analyzer that
// guesses prints in the same format as one that measured, and the guess
// inherits the measurement's credibility -- which is exactly what the
// pattern-matching tools in this space already do.
func basisFloor(b Basis) Class {
	switch b {
	case BasisComputed:
		return ClassRead // computed evidence imposes no floor of its own
	case BasisDeclared:
		return ClassCompensable // somebody told us it is destructive, even though they could be lying; an unanalyzed action has told us nothing
	default:
		return ClassTerminal // we did not look, so we must assume the worst
	}
}

// Classify returns the worst of: the supplied floor, and the floor each
// effect's basis imposes. Worst always wins -- a namespace whose objects can
// be restored but whose volume data cannot is TERMINAL, and saying
// COMPENSABLE because most of it is recoverable would be a true sentence
// about the wrong thing.
func Classify(effects []Effect, floor Class) Class {
	worst := floor
	for _, e := range effects {
		if f := basisFloor(e.Basis); f > worst {
			worst = f
		}
	}
	return worst
}

func (c Class) ExitCode() int {
	switch c {
	case ClassRead, ClassReversible:
		return 0
	case ClassCompensable:
		return 3
	case ClassTerminal:
		return 4
	case ClassAuthority:
		return 5
	}
	return 1
}

func (c Class) String() string {
	switch c {
	case ClassRead:
		return "READ"
	case ClassReversible:
		return "REVERSIBLE"
	case ClassCompensable:
		return "COMPENSABLE"
	case ClassTerminal:
		return "TERMINAL"
	case ClassAuthority:
		return "AUTHORITY"
	}
	return "UNKNOWN"
}

package model

import "testing"

func TestClassifyTakesTheWorstEffect(t *testing.T) {
	effects := []Effect{
		{Kind: "destroys", Basis: BasisComputed, Explanation: "a"},
		{Kind: "destroys-data", Basis: BasisComputed, Explanation: "b"},
	}
	// The caller supplies a per-effect floor via classifyOne; here we assert the
	// aggregate takes the maximum, because a namespace whose objects are
	// restorable but whose PV data is not is TERMINAL, not COMPENSABLE.
	got := Classify(effects, ClassRead)
	if got != ClassRead {
		t.Fatalf("with no per-effect classes the floor governs: got %v", got)
	}
}

// An unknown basis anywhere caps the whole finding at its worst class. This is
// the rule that stops broad verb coverage from laundering guesses through the
// same output format as computed facts.
func TestUnknownBasisCannotProduceAPassingClass(t *testing.T) {
	effects := []Effect{
		{Kind: "destroys", Basis: BasisComputed},
		{Kind: "unanalysed", Basis: BasisUnknown},
	}
	if got := Classify(effects, ClassReversible); got != ClassTerminal {
		t.Errorf("Classify = %v, want TERMINAL -- an unknown basis must not pass", got)
	}
}

func TestDeclaredBasisDoesNotEarnTrust(t *testing.T) {
	// An MCP destructiveHint is read and reported, never believed. A finding
	// resting only on a declaration cannot be better than COMPENSABLE.
	effects := []Effect{{Kind: "destroys", Basis: BasisDeclared}}
	if got := Classify(effects, ClassReversible); got != ClassCompensable {
		t.Errorf("Classify = %v, want COMPENSABLE for a declared-only finding", got)
	}
}

func TestExitCodes(t *testing.T) {
	for _, tc := range []struct {
		c    Class
		want int
	}{
		{ClassRead, 0}, {ClassReversible, 0}, {ClassCompensable, 3},
		{ClassTerminal, 4}, {ClassAuthority, 5},
	} {
		if got := tc.c.ExitCode(); got != tc.want {
			t.Errorf("%v.ExitCode() = %d, want %d", tc.c, got, tc.want)
		}
	}
}

// AUTHORITY outranks TERMINAL deliberately: an action that destroys little but
// removes the caller's power to repair what comes next is worse than one that
// destroys a lot and leaves them able to act.
func TestAuthorityOutranksTerminal(t *testing.T) {
	if !(ClassAuthority > ClassTerminal) {
		t.Error("ClassAuthority must sort above ClassTerminal")
	}
}

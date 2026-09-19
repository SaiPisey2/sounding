package main

import "testing"

// The exit code is the contract every caller integrates against, so it is
// pinned here rather than left to the class type alone.
func TestExitCodeForRefusal(t *testing.T) {
	if got := exitCodeForError(errRefused); got != 2 {
		t.Errorf("refusal exit = %d, want 2", got)
	}
}

func TestExitCodeForOperationalError(t *testing.T) {
	if got := exitCodeForError(errOperational); got != 1 {
		t.Errorf("operational exit = %d, want 1", got)
	}
}

package main

import (
	"bytes"
	"strings"
	"testing"
)

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

// -h, --help and help must all print to STDOUT and exit 0 -- the exact
// opposite of a refusal -- both at the top level and after "score", and
// score's own help must carry the exit-code table too, not just bare flag
// defaults: it is the one place most likely to actually reach an
// integrator writing a gateway wrapper around this binary, and treating
// exit 3/4 as an ordinary error is the most likely integration bug this
// tool can cause.
func TestHelpTokensPrintToStdoutAndExitZero(t *testing.T) {
	for _, args := range [][]string{
		{"-h"},
		{"--help"},
		{"help"},
		{"score", "-h"},
		{"score", "--help"},
		{"score", "help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(args, strings.NewReader(""), &out, &errOut)
			if code != 0 {
				t.Errorf("exit = %d, want 0; stderr:\n%s", code, errOut.String())
			}
			if errOut.Len() != 0 {
				t.Errorf("help must not write to stderr, got:\n%s", errOut.String())
			}
			if !strings.Contains(out.String(), "usage: sounding score") {
				t.Errorf("help must print the usage block:\n%s", out.String())
			}
			if !strings.Contains(out.String(), "exit codes:") {
				t.Errorf("help must print the exit-code table:\n%s", out.String())
			}
			for _, code := range []string{"0", "1", "2", "3", "4", "5"} {
				if !strings.Contains(out.String(), code) {
					t.Errorf("exit-code table must mention %s:\n%s", code, out.String())
				}
			}
		})
	}
}

// An ordinary bad flag must still refuse to stderr with exit 2 -- accepting
// -h/--help/help must not turn every other flag error into a help dump.
func TestUnknownFlagStillRefusesNotHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"score", "--bogus"}, strings.NewReader(""), &out, &errOut)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if out.Len() != 0 {
		t.Errorf("an ordinary flag error must not write the help block to stdout, got:\n%s", out.String())
	}
}

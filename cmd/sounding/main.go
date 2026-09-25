// Command sounding scores a Kubernetes mutation against live cluster state
// and reports what it would destroy. It issues no write, delete or patch
// against a cluster -- it needs list and get, nothing else. A cluster-admin
// credential handed to this binary could still perform the mutation being
// scored; scope it to list and get so that it cannot.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/SaiPisey2/sounding/internal/action"
	"github.com/SaiPisey2/sounding/internal/report"
	"github.com/SaiPisey2/sounding/pkg/cluster"
	"github.com/SaiPisey2/sounding/pkg/model"
	"github.com/SaiPisey2/sounding/pkg/score"
)

// The two outcomes are defined by pkg/score, which produces them; the
// command only maps them to exit codes.
var (
	errRefused     = score.ErrRefused
	errOperational = score.ErrOperational
)

// exitCodeForError is the one place a Go error becomes a process exit
// code. It is tested directly because the exit code -- not the error
// value, which no caller outside this process ever sees -- is the actual
// contract every integration is written against.
func exitCodeForError(err error) int {
	switch {
	case errors.Is(err, errRefused):
		return 2
	case errors.Is(err, errOperational):
		return 1
	default:
		return 1
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

const usage = "usage: sounding score '<command>' [--snapshot DIR] [--kubeconfig PATH] [--json] [--all]\n" +
	"       sounding score --stdin [--snapshot DIR] [--kubeconfig PATH] [--json] [--all]"

// exitCodeTable documents the class -> exit code contract in the one place
// most likely to actually reach an integrator writing a gateway wrapper
// around this binary: the binary itself, on both `-h` and `score -h`,
// rather than only the README, which that integration code never reads at
// run time. Treating exit 3 (COMPENSABLE) or 4 (TERMINAL) as an ordinary
// tool error rather than a verdict is the single most likely integration
// bug this tool can cause -- it would mean the wrapper's approval or retry
// logic never sees the one thing sounding exists to report.
const exitCodeTable = `exit codes:
  0   READ or REVERSIBLE -- nothing destructive was found, or it was found but is trivially undone
  1   operational error -- sounding itself failed to run (bad kubeconfig, network error, disk write failure); fix the environment, not the command
  2   refused -- the command could not be scored (bad input, an unresolvable resource, a namespace that does not exist, a cluster discovery could not fully enumerate); nothing was scored
  3   COMPENSABLE -- destructive; every effect is restorable
  4   TERMINAL -- at least one effect's data cannot come back
  5   AUTHORITY -- the action itself changes who can act, not just what exists
`

// isHelpToken and containsHelpToken recognise every spelling of "show me
// help" this build accepts. All three are honoured identically at the top
// level and after "score": a caller who reaches for -h, --help or help
// should get the same answer regardless of which one they reached for or
// where in the arguments they put it.
func isHelpToken(s string) bool {
	return s == "-h" || s == "--help" || s == "help"
}

func containsHelpToken(toks []string) bool {
	for _, t := range toks {
		if isHelpToken(t) {
			return true
		}
	}
	return false
}

// printTopLevelHelp and printScoreHelp write to STDOUT and are the exit-0
// path, deliberately unlike every refusal in this file, which writes to
// stderr and exits 2 -- a caller piping --help into a pager, or checking
// $? after asking for it, must not be told the request itself failed.
func printTopLevelHelp(w io.Writer) {
	fmt.Fprintln(w, usage)
	fmt.Fprintln(w)
	fmt.Fprint(w, exitCodeTable)
}

func printScoreHelp(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, usage)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "flags:")
	out := fs.Output()
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(out)
	fmt.Fprintln(w)
	fmt.Fprint(w, exitCodeTable)
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "refused: no subcommand given")
		fmt.Fprintln(stderr, usage)
		return exitCodeForError(errRefused)
	}
	if isHelpToken(args[0]) {
		printTopLevelHelp(stdout)
		return 0
	}
	if args[0] != "score" {
		fmt.Fprintf(stderr, "refused: unknown subcommand %q\n", args[0])
		fmt.Fprintln(stderr, usage)
		return exitCodeForError(errRefused)
	}

	// The command string, when present, is always the first token after
	// "score" -- flags follow it. That lets the positional argument be
	// pulled off by hand before handing the rest to the standard flag
	// package, which would otherwise stop parsing at the first non-flag
	// argument it sees and never reach the flags that follow it.
	rest := args[1:]
	var command string
	haveCommand := len(rest) > 0 && !strings.HasPrefix(rest[0], "-")
	if haveCommand {
		command = rest[0]
		rest = rest[1:]
	}

	fs := flag.NewFlagSet("score", flag.ContinueOnError)
	fs.SetOutput(stderr)
	snapshotDir := fs.String("snapshot", "", "write an undo bundle of every object that would be destroyed to this directory")
	kubeconfig := fs.String("kubeconfig", "", "path to a kubeconfig file; empty means the in-cluster config or the environment default")
	jsonOut := fs.Bool("json", false, "print the finding as JSON instead of the human-readable report")
	allEffects := fs.Bool("all", false, "list every effect in the human-readable report instead of the default cap")
	useStdin := fs.Bool("stdin", false, "read a JSON-encoded Action from stdin instead of a command string")

	// Checked before fs.Parse, and against command/rest directly rather than
	// relying on the flag package's own -h/-help handling: that path prints
	// bare flag defaults to stderr and returns flag.ErrHelp, which this
	// build would otherwise turn into a refusal and exit 2 -- the opposite
	// of what asking for help should ever do.
	if command == "help" || containsHelpToken(rest) {
		printScoreHelp(stdout, fs)
		return 0
	}

	if err := fs.Parse(rest); err != nil {
		return exitCodeForError(errRefused)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "refused: unexpected arguments after flags: %v\n", fs.Args())
		return exitCodeForError(errRefused)
	}
	if haveCommand == *useStdin {
		fmt.Fprintln(stderr, "refused: score needs exactly one of a command string or --stdin")
		return exitCodeForError(errRefused)
	}

	var act model.Action
	var err error
	if *useStdin {
		act, err = action.ReadJSON(stdin)
	} else {
		act, err = action.ParseCommand(command)
	}
	if err != nil {
		refusal := fmt.Errorf("%w: %w", errRefused, err)
		fmt.Fprintf(stderr, "%v\n", refusal)
		return exitCodeForError(refusal)
	}

	c, err := cluster.New(*kubeconfig)
	if err != nil {
		err = fmt.Errorf("%w: building cluster clients: %v", errOperational, err)
		fmt.Fprintf(stderr, "%v\n", err)
		return exitCodeForError(err)
	}
	finding, err := score.Score(context.Background(), c, act, score.Options{SnapshotDir: *snapshotDir})
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitCodeForError(err)
	}

	switch {
	case *jsonOut:
		// --json was never capped: it encodes every effect regardless of
		// --all, since a machine reader piping structured output does not
		// scroll past a safety line the way a terminal does.
		if err := writeJSON(stdout, finding); err != nil {
			wrapped := fmt.Errorf("%w: encoding finding as json: %v", errOperational, err)
			fmt.Fprintf(stderr, "%v\n", wrapped)
			return exitCodeForError(wrapped)
		}
	case *allEffects:
		report.WriteAll(stdout, finding)
	default:
		report.Write(stdout, finding)
	}

	// Only path by which the process exits 0/3/4/5: a completed scan. Every
	// other return above this line is an explicit refusal (2) or
	// operational failure (1), never a class the scan didn't actually
	// reach.
	return finding.Class.ExitCode()
}

// jsonFinding mirrors model.Finding for --json output, with one deliberate
// difference: Class is rendered as its name, not encoded as-is. model.Class
// is an int underneath, and encoding it directly would print e.g. 3 for a
// TERMINAL finding -- which is COMPENSABLE's own exit code. A number that
// silently means a different class depending on which line of the report
// you compare it to is worse than an unsupported flag would have been.
type jsonFinding struct {
	Action   model.Action    `json:"action"`
	Effects  []model.Effect  `json:"effects"`
	Class    string          `json:"class"`
	Undo     *model.UndoPlan `json:"undo,omitempty"`
	Scanned  time.Time       `json:"scanned"`
	APICalls int             `json:"apiCalls"`
}

func writeJSON(w io.Writer, f model.Finding) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(jsonFinding{
		Action:   f.Action,
		Effects:  f.Effects,
		Class:    f.Class.String(),
		Undo:     f.Undo,
		Scanned:  f.Scanned,
		APICalls: f.APICalls,
	})
}

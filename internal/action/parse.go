package action

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/SaiPisey2/sounding/pkg/model"
)

var ErrAmbiguous = errors.New("ambiguous command")

// ParseCommand parses a delete command from a string. It supports the subset
// of kubectl delete commands necessary for v1: delete namespace, delete ns,
// and delete <resource> <name> with optional -n/--namespace flags. Any command
// whose meaning cannot be recovered from the string alone is refused.
func ParseCommand(s string) (model.Action, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return model.Action{}, fmt.Errorf("%w: empty command", ErrAmbiguous)
	}

	tokens := strings.Fields(s)

	// Strip leading "kubectl" if present.
	if len(tokens) > 0 && tokens[0] == "kubectl" {
		tokens = tokens[1:]
	}

	if len(tokens) == 0 {
		return model.Action{}, fmt.Errorf("%w: empty command", ErrAmbiguous)
	}

	// Require the verb to be "delete".
	if tokens[0] != "delete" {
		return model.Action{}, fmt.Errorf("%w: only delete is supported, got %q", ErrAmbiguous, tokens[0])
	}

	tokens = tokens[1:]

	if len(tokens) == 0 {
		return model.Action{}, fmt.Errorf("%w: delete requires a resource or namespace", ErrAmbiguous)
	}

	// Parse the resource and name(s), handling the special case of namespace/ns.
	var resource, name, namespace string
	var namespaceSet bool
	var idx int

	// Check if the first token is a flag (error case).
	if strings.HasPrefix(tokens[0], "-") {
		// Handle specific flags early to give better error messages.
		switch tokens[0] {
		case "-f", "--filename":
			return model.Action{}, fmt.Errorf("%w: -f/--filename not supported", ErrAmbiguous)
		case "-l", "--selector":
			return model.Action{}, fmt.Errorf("%w: -l/--selector not supported", ErrAmbiguous)
		case "--all":
			return model.Action{}, fmt.Errorf("%w: --all not supported", ErrAmbiguous)
		case "--all-namespaces":
			return model.Action{}, fmt.Errorf("%w: --all-namespaces not supported", ErrAmbiguous)
		default:
			return model.Action{}, fmt.Errorf("%w: expected resource, got flag %q", ErrAmbiguous, tokens[0])
		}
	}

	// "namespace"/"ns" is the one resource this parser normalises itself,
	// because it is not a guess: it is this tool's own hard-coded name for
	// the single cluster-scoped kind it understands, and both spellings
	// mean exactly the same thing regardless of what a live cluster calls
	// anything else. Every other resource is kept EXACTLY as typed, with no
	// pluralisation attempted anywhere in this function. It is handed to
	// cluster.ResolveResource later, which matches it against a live
	// server's real plural, singular and short names; guessing a plural
	// here would only hand the resolver a wrong string instead of the
	// right one -- a different bug with the same shape as the one this
	// replaces.
	if tokens[0] == "namespace" || tokens[0] == "ns" {
		resource = "namespaces"
		idx = 1
	} else {
		resource = tokens[0]
		idx = 1
	}

	if idx >= len(tokens) {
		return model.Action{}, fmt.Errorf("%w: %s requires a name", ErrAmbiguous, tokens[0])
	}

	// The next token should be the name (unless it's a flag).
	if strings.HasPrefix(tokens[idx], "-") {
		return model.Action{}, fmt.Errorf("%w: expected name, got flag %q", ErrAmbiguous, tokens[idx])
	}

	name = tokens[idx]
	idx++

	// Check for additional positional arguments (not allowed).
	if idx < len(tokens) && !strings.HasPrefix(tokens[idx], "-") {
		return model.Action{}, fmt.Errorf("%w: multiple names not supported", ErrAmbiguous)
	}

	// Parse flags.
	for idx < len(tokens) {
		flag := tokens[idx]
		idx++

		switch flag {
		case "-n", "--namespace":
			if idx >= len(tokens) {
				return model.Action{}, fmt.Errorf("%w: flag %s requires a value", ErrAmbiguous, flag)
			}
			newNamespace := tokens[idx]
			idx++
			if namespaceSet && newNamespace != namespace {
				return model.Action{}, fmt.Errorf("%w: conflicting namespace flags: %q and %q", ErrAmbiguous, namespace, newNamespace)
			}
			namespace = newNamespace
			namespaceSet = true
		default:
			if strings.HasPrefix(flag, "-") {
				return model.Action{}, fmt.Errorf("%w: unrecognized flag %q", ErrAmbiguous, flag)
			}
			return model.Action{}, fmt.Errorf("%w: unexpected positional argument %q", ErrAmbiguous, flag)
		}
	}

	return model.Action{
		Verb: "delete",
		Target: model.Target{
			Resource:  resource,
			Namespace: namespace,
			Name:      name,
		},
	}, nil
}

// maxActionBody bounds how much of stdin ReadJSON will read: enough for any
// real Action, small enough that a caller who pipes in the wrong file (or
// an unbounded stream) fails fast rather than after reading it all.
const maxActionBody = 1024 * 1024 // 1 MiB

// ReadJSON parses a delete action from JSON. It accepts any verb; refusal of
// unsupported verbs belongs to the analyzer, not the parser, so the report can
// name which verb was not handled rather than saying "could not parse".
// It rejects a JSON body with no verb, since that represents the absence of
// an action, not an action with an unanalyzed verb.
func ReadJSON(r io.Reader) (model.Action, error) {
	// Read one byte past the cap: exactly maxActionBody bytes back means the
	// body might be larger still (this read simply stopped at the limit),
	// while maxActionBody+1 read in full proves it actually is. Decoding
	// straight off a LimitReader instead would just hand json.Decode a
	// truncated document, which fails as "unexpected EOF" -- a real error,
	// but one that names the wrong cause: the caller sent too much, not
	// something that failed to parse.
	body, err := io.ReadAll(io.LimitReader(r, maxActionBody+1))
	if err != nil {
		return model.Action{}, fmt.Errorf("%w: reading JSON body: %v", ErrAmbiguous, err)
	}
	if len(body) > maxActionBody {
		return model.Action{}, fmt.Errorf("%w: JSON body exceeds the %d byte (1 MiB) limit", ErrAmbiguous, maxActionBody)
	}

	var a model.Action
	if err := json.Unmarshal(body, &a); err != nil {
		return model.Action{}, fmt.Errorf("%w: invalid JSON: %v", ErrAmbiguous, err)
	}

	if a.Verb == "" {
		return model.Action{}, fmt.Errorf("%w: JSON body carried no verb", ErrAmbiguous)
	}

	return a, nil
}

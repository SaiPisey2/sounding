package action

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/SaiPisey2/sounding/internal/model"
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

	// Check if the first token is "namespace" or "ns".
	if tokens[0] == "namespace" || tokens[0] == "ns" {
		resource = "namespaces"
		idx = 1
	} else {
		// It's a regular resource.
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
			namespace = tokens[idx]
			idx++
		case "-f", "--filename":
			return model.Action{}, fmt.Errorf("%w: -f/--filename not supported", ErrAmbiguous)
		case "-l", "--selector":
			return model.Action{}, fmt.Errorf("%w: -l/--selector not supported", ErrAmbiguous)
		case "--all":
			return model.Action{}, fmt.Errorf("%w: --all not supported", ErrAmbiguous)
		case "--all-namespaces":
			return model.Action{}, fmt.Errorf("%w: --all-namespaces not supported", ErrAmbiguous)
		default:
			if strings.HasPrefix(flag, "-") {
				return model.Action{}, fmt.Errorf("%w: unrecognized flag %q", ErrAmbiguous, flag)
			}
			return model.Action{}, fmt.Errorf("%w: unexpected positional argument %q", ErrAmbiguous, flag)
		}
	}

	// Normalize the resource name: ns -> namespaces, singular -> plural.
	if resource == "ns" {
		resource = "namespaces"
	} else if !strings.HasSuffix(resource, "s") {
		// Best-effort pluralization: append 's' if not already plural.
		// Note: This is incorrect for irregular plurals like 'ingress' -> 'ingresses'
		// and 'endpoints'. Task 3's discovery data carries the real plural and singular
		// names, and will resolve this against the live API, refusing if it does not
		// match exactly one known resource.
		resource = resource + "s"
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

// ReadJSON parses a delete action from JSON. It accepts any verb; refusal of
// unsupported verbs belongs to the analyzer, not the parser, so the report can
// name which verb was not handled rather than saying "could not parse".
func ReadJSON(r io.Reader) (model.Action, error) {
	var a model.Action
	if err := json.NewDecoder(r).Decode(&a); err != nil {
		return model.Action{}, fmt.Errorf("%w: invalid JSON: %v", ErrAmbiguous, err)
	}
	return a, nil
}

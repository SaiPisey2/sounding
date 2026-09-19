package action

import (
	"errors"
	"strings"
	"testing"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		name, in                  string
		wantRes, wantNS, wantName string
	}{
		{"namespace long form", "delete namespace prod-payments", "namespaces", "", "prod-payments"},
		{"namespace alias", "delete ns prod-payments", "namespaces", "", "prod-payments"},
		{"with kubectl prefix", "kubectl delete ns prod-payments", "namespaces", "", "prod-payments"},
		{"namespaced resource", "delete deployment api -n prod", "deployments", "prod", "api"},
		{"long namespace flag", "delete deployment api --namespace prod", "deployments", "prod", "api"},
		{"singular is normalised", "delete deployments api -n prod", "deployments", "prod", "api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := ParseCommand(tc.in)
			if err != nil {
				t.Fatalf("ParseCommand(%q) errored: %v", tc.in, err)
			}
			if a.Verb != "delete" {
				t.Errorf("Verb = %q, want delete", a.Verb)
			}
			if a.Target.Resource != tc.wantRes {
				t.Errorf("Resource = %q, want %q", a.Target.Resource, tc.wantRes)
			}
			if a.Target.Namespace != tc.wantNS {
				t.Errorf("Namespace = %q, want %q", a.Target.Namespace, tc.wantNS)
			}
			if a.Target.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", a.Target.Name, tc.wantName)
			}
		})
	}
}

// Every one of these is a command whose blast radius is real but whose meaning
// cannot be recovered from the string alone. Refusing is the whole point: a
// tool that guesses here is the pattern matcher it was built to replace.
func TestParseCommandRefusesWhatItCannotResolve(t *testing.T) {
	for _, in := range []string{
		"delete -f manifest.yaml",
		"delete --filename manifest.yaml",
		"delete pods -l app=api -n prod",
		"delete pods --selector app=api -n prod",
		"delete pods --all -n prod",
		"delete ns --all",
		"delete deployment api web -n prod",
		"scale deployment api --replicas=0 -n prod",
		"apply -f manifest.yaml",
		"",
	} {
		t.Run(in, func(t *testing.T) {
			_, err := ParseCommand(in)
			if err == nil {
				t.Fatalf("ParseCommand(%q) succeeded, want refusal", in)
			}
			if !errors.Is(err, ErrAmbiguous) {
				t.Errorf("error %v does not wrap ErrAmbiguous", err)
			}
			if strings.TrimSpace(err.Error()) == "" {
				t.Error("refusal must name its cause")
			}
		})
	}
}

// Duplicate namespace flags cannot be resolved: which value did the caller
// intend? Refusing names both values so the caller can be specific.
func TestParseCommandRefusesDuplicateNamespaceFlag(t *testing.T) {
	_, err := ParseCommand("delete deployment api -n a -n b")
	if err == nil {
		t.Fatalf("ParseCommand with duplicate -n flags succeeded, want refusal")
	}
	if !errors.Is(err, ErrAmbiguous) {
		t.Errorf("error %v does not wrap ErrAmbiguous", err)
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Errorf("refusal must name both namespace values; got %q", err.Error())
	}
}

func TestReadJSON(t *testing.T) {
	in := `{"verb":"delete","target":{"resource":"namespaces","name":"prod-payments"}}`
	a, err := ReadJSON(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ReadJSON errored: %v", err)
	}
	if a.Verb != "delete" || a.Target.Name != "prod-payments" {
		t.Errorf("got %+v", a)
	}
}

// A JSON action naming a verb no analyzer covers must be readable -- the
// refusal belongs to the analyzer, not the parser, so the report can say
// which verb was unanalysed rather than "could not parse".
func TestReadJSONAcceptsAnyVerb(t *testing.T) {
	a, err := ReadJSON(strings.NewReader(`{"verb":"patch","target":{"resource":"services","name":"api"}}`))
	if err != nil {
		t.Fatalf("ReadJSON errored: %v", err)
	}
	if a.Verb != "patch" {
		t.Errorf("Verb = %q, want patch", a.Verb)
	}
}

// An empty verb is not a verb no analyzer covers; it is the absence of one.
// That is a parse-time fact: the JSON carried no meaningful action.
func TestReadJSONRefusesEmptyVerb(t *testing.T) {
	cases := []string{
		`null`,
		`{}`,
		`{"target":{"resource":"services","name":"api"}}`,
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := ReadJSON(strings.NewReader(in))
			if err == nil {
				t.Fatalf("ReadJSON(%q) succeeded, want refusal", in)
			}
			if !errors.Is(err, ErrAmbiguous) {
				t.Errorf("error %v does not wrap ErrAmbiguous", err)
			}
			if !strings.Contains(err.Error(), "verb") {
				t.Errorf("refusal must name the missing verb; got %q", err.Error())
			}
		})
	}
}

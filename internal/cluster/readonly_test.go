package cluster

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mutatingSelectors names every client-go method that performs a write,
// delete or patch against a cluster. It is matched against the SELECTOR of
// a call expression -- the "Foo" in "x.Foo(...)" -- not against source text,
// so a comment or a doc string that happens to contain one of these words as
// prose can never trip it, and a method call is caught regardless of what
// package or wrapper type it is reached through (Dynamic, Typed, Metadata,
// or anything built on top of them later).
//
// EvictV1 (and the older, deprecated Evict) delete a running pod exactly as
// surely as Delete deletes any other object; Apply and ApplyStatus are
// server-side apply, a write by another name; Post and Put are the raw HTTP
// verbs a hand-built REST request issues directly, bypassing every other
// name on this list.
var mutatingSelectors = map[string]bool{
	"Create":           true,
	"Update":           true,
	"UpdateStatus":     true,
	"Patch":            true,
	"Apply":            true,
	"ApplyStatus":      true,
	"Delete":           true,
	"DeleteCollection": true,
	"Evict":            true,
	"EvictV1":          true,
	"Post":             true,
	"Put":              true,
}

// mutatingVerbStrings names the string arguments to a raw RESTClient's
// Verb(...) call that perform a write, delete or patch. Verb("GET") and
// Verb("LIST") build a read; nothing here bans those, or Verb() itself --
// only the specific strings that turn it into a mutation.
var mutatingVerbStrings = map[string]bool{
	"POST":   true,
	"PUT":    true,
	"PATCH":  true,
	"DELETE": true,
}

// The tool scores destructive actions. It must not be able to perform one.
// This walks the WHOLE repository rather than trusting review to notice --
// not just internal/, which would miss cmd/sounding, the package that
// actually builds the clients and wires every other package together. A
// mutating call planted there is exactly as dangerous as one planted in
// internal/cluster itself, and a walker that only covers internal/ would
// let it ship silently.
//
// This parses each file with go/ast and inspects call expressions rather
// than matching source text: a substring scan of whole files, comments
// included, let five real write paths (Apply, UpdateStatus, EvictV1, a raw
// RESTClient Verb("DELETE"), and RESTClient().Post()) compile straight
// through a fully green suite, and separately false-positived on prose in
// this file's own explanatory comments. Matching selector expressions in
// the parsed syntax tree cannot see a comment at all, and catches a
// mutating method call regardless of which client, wrapper, or spelling of
// "delete a pod" it arrives through.
func TestNoMutatingClientCallsAnywhere(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// .git holds compressed object data, not source, and walking it
			// is both pointless and slow.
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if mutatingSelectors[sel.Sel.Name] {
				t.Errorf("%s: calls .%s(...) -- this tool is read-only by construction",
					fset.Position(call.Pos()), sel.Sel.Name)
				return true
			}
			if sel.Sel.Name == "Verb" && len(call.Args) == 1 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					verb := strings.Trim(lit.Value, `"`)
					if mutatingVerbStrings[strings.ToUpper(verb)] {
						t.Errorf("%s: calls .Verb(%s) -- this tool is read-only by construction",
							fset.Position(call.Pos()), lit.Value)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

package cluster

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// clientFields names every field on Clients (see client.go) that reaches a
// live cluster: Typed, Dynamic, Discovery and Metadata. Calls is
// deliberately absent -- it is a *int64 request counter, not a client, and
// reaches no API server no matter what is done to it.
var clientFields = map[string]bool{
	"Typed":     true,
	"Dynamic":   true,
	"Discovery": true,
	"Metadata":  true,
}

// allowedSelectors is the complete set of method names this build may
// invoke through a client, once the call's receiver chain is confirmed to
// be rooted at one of clientFields (see rooted, below). An AST census of
// every non-test file in the repository -- every call whose receiver chain
// resolves back to Typed, Dynamic, Discovery or Metadata -- found exactly
// two: Get and List.
//
// (ListableNamespaced, the discovery helper's own name, is a package-level
// function -- cluster.ListableNamespaced(ctx, c) -- whose receiver chain is
// the "cluster" package, not a client field; it is out of this allowlist's
// scope for the same reason fmt.Sprintf and os.WriteFile are. Its own
// body's one client-touching line --
// discovery.ServerPreferredNamespacedResourcesWithContext(ctx, c.Discovery)
// -- passes c.Discovery as a plain argument rather than as a receiver,
// which this walker cannot see either; see this file's own top-level test
// doc comment for what that costs.)
//
// This is an ALLOWLIST, not the denylist it replaces: anything not named
// here is refused, including a real client-go method this build has never
// had reason to call. That is the point -- the denylist enumerated what to
// reject and missed both new and existing write paths by name; this
// enumerates what to accept, and today's shipped code satisfies it with
// exactly two names.
var allowedSelectors = map[string]bool{
	"Get":  true,
	"List": true,
}

// allowedVerbArgs is Verb's own allowlist: the literal string arguments a
// raw RESTClient's Verb(...) call may carry. Nothing in this build calls
// Verb today (the census found none), but the read/write distinction a raw
// REST verb draws is orthogonal to method names entirely, so Verb gets its
// own rule -- see checkSelector -- rather than being folded into
// allowedSelectors, where its presence would say nothing about which
// argument makes a given call safe.
var allowedVerbArgs = map[string]bool{
	"GET":  true,
	"LIST": true,
}

// TestClientCallsAreLimitedToTheReadAllowlist replaces a denylist that
// named twelve mutating method names with an allowlist of two. The denylist
// missed eight compiling write paths in review before this test was
// written: UpdateScale, CreateToken, UpdateEphemeralContainers, ApplyScale
// and UpdateApproval (matched by exact name only, so a call merely starting
// with a listed word slipped through), Bind (simply never added to the
// list), a raw RESTClient's Verb(...) called with anything but a string
// literal (a value built from concatenation or runtime uppercasing reads
// identically to a hardcoded write verb to a walker that only inspects
// *ast.BasicLit), and a method value -- `del :=
// c.Typed.CoreV1().ConfigMaps("x").Delete; del(ctx, ...)` -- which defeats
// every name on any denylist at once, because the mutating call site is
// `del(ctx, ...)`, whose Fun is a plain identifier naming nothing a
// selector-based walker was ever looking at.
//
// The allowlist closes all of these the same way: it does not ask "is this
// name one we already know is bad" -- a question a new spelling always
// answers "no" to -- it asks "is this expression's receiver chain rooted at
// a client field, and if so, is its final name one of the two this build
// actually uses" -- a question an unrecognised name answers correctly by
// default, by failing. The method-value evasion is caught by judging every
// ROOTED SELECTOR EXPRESSION this way, not only ones immediately followed
// by a call, so `del := ....Delete` is refused at the assignment, before
// `del` is ever called.
//
// Scope, precisely: an expression is inspected only if its receiver chain
// resolves back to one of Typed/Dynamic/Discovery/Metadata -- the Clients
// struct's own client fields (client.go) -- through any number of chained
// calls (c.Typed.CoreV1().Pods(ns) is rooted three levels down) or through
// a local variable directly assigned from one of them (typed := c.Typed;
// typed.CoreV1()... is rooted through typed). Anything else -- fmt.Sprintf,
// os.WriteFile, a field access on an unrelated struct that happens to share
// one of these field names -- is out of scope and never inspected, which is
// what keeps this from re-creating the denylist's opposite failure:
// flagging an ordinary stdlib call by name alone. (The old denylist would
// have flagged a hypothetical os.Create call here purely because "Create"
// was on its list; this allowlist does not look at os.Create at all,
// because os is not a client field and nothing assigns a variable from one
// named os.)
//
// What this does NOT catch, structurally rather than by oversight: a
// mutating call reached through a client passed as a plain function
// ARGUMENT rather than as a receiver -- helper(c.Typed), where helper's own
// body then calls iface.Delete(...) on its parameter -- would slip through
// undetected, because nothing assigns helper's parameter from a rooted
// expression in source this walker can see; a parameter's own value flows
// from its caller, invisible without type or interprocedural analysis. No
// function in this repository takes a bare client interface as a parameter
// today -- every one takes *cluster.Clients and reaches Typed, Dynamic,
// Discovery or Metadata directly off it, which IS caught, because the
// field-name rule fires on `c.Typed` regardless of whether `c` is a
// parameter, a local variable, or anything else -- but a function written
// the other way in the future would need its own review, not just this
// test passing. Full type resolution (go/types, with an importer that
// pulls in k8s.io/client-go's real interfaces) would close this gap; it is
// left out because the module constraint here forbids a new dependency,
// and a stdlib-only importer capable of resolving this repository's actual
// build graph is disproportionate to a gap with no live instance to close.
// The same limitation means a call reached only through an interface value
// of unknown dynamic type is invisible to this walker in general -- it
// judges syntax, not types, everywhere.
func TestClientCallsAreLimitedToTheReadAllowlist(t *testing.T) {
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
		checkFile(t, fset, file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// checkFile inspects one parsed file's function bodies. Rooted-variable
// tracking (rootedVars) is scoped per function, not per file, so a variable
// name reused for something unrelated to a client in a different function
// cannot leak a false "rooted" verdict across function boundaries. The
// imprecision that remains -- no block scoping within one function, and a
// closure's locals are folded into its enclosing function's set -- only
// ever makes this test STRICTER, never more permissive: at worst it asks a
// future developer to justify a false alarm, which is the safe direction
// for a guard whose job is refusing a mutation.
func checkFile(t *testing.T, fset *token.FileSet, file *ast.File) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		parents := buildParents(fn.Body)
		rv := rootedVars(fn.Body)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || !rooted(sel.X, rv) {
				return true
			}
			checkSelector(t, fset, parents, sel)
			return true
		})
	}
}

// checkSelector judges one rooted selector expression -- c.Typed.Foo, in
// any position, called or not. Verb carries its own rule regardless of
// position, because a raw REST verb is worth checking the moment it is
// reached, not only when it turns out to be this expression's final call.
// Every other name is judged only when it is NOT mid-chain (see midChain):
// an intermediate call like CoreV1() or Namespace(ns) is navigation, not a
// verb, and must never be compared against allowedSelectors itself.
func checkSelector(t *testing.T, fset *token.FileSet, parents map[ast.Node]ast.Node, sel *ast.SelectorExpr) {
	pos := fset.Position(sel.Pos())

	if sel.Sel.Name == "Verb" {
		call, calledHere := callOf(parents, sel)
		if !calledHere || !isAllowedVerbCall(call) {
			t.Errorf("%s: .Verb(...) is reached through a client with an argument outside the read allowlist (GET, LIST), or is referenced without being called with one -- this tool is read-only by construction", pos)
		}
		return
	}

	if allowedSelectors[sel.Sel.Name] {
		return
	}
	if midChain(parents, sel) {
		return
	}
	t.Errorf("%s: .%s is reached through a client -- not in the read allowlist (Get, List) -- this tool is read-only by construction", pos, sel.Sel.Name)
}

// callOf reports the *ast.CallExpr that calls sel directly (sel is exactly
// its Fun), if any -- distinguishing `x.Verb("GET")` from a bare reference
// to `x.Verb` captured as a value and never called with a literal at all.
func callOf(parents map[ast.Node]ast.Node, sel *ast.SelectorExpr) (*ast.CallExpr, bool) {
	call, ok := parents[sel].(*ast.CallExpr)
	if !ok || call.Fun != sel {
		return nil, false
	}
	return call, true
}

// midChain reports whether sel is called, and that call's own result is
// immediately selector-accessed again (CoreV1() feeding .Namespaces(),
// .Namespaces() feeding .Get(...)) -- i.e. whether sel names a navigational
// step rather than the chain's actual final call.
func midChain(parents map[ast.Node]ast.Node, sel *ast.SelectorExpr) bool {
	call, ok := callOf(parents, sel)
	if !ok {
		return false
	}
	outer, ok := parents[call].(*ast.SelectorExpr)
	return ok && outer.X == call
}

// isAllowedVerbCall reports whether call is `.Verb("GET")` or
// `.Verb("LIST")` -- exactly one argument, a string literal (not a
// concatenation, not a runtime-computed string, not a variable), whose
// value is in allowedVerbArgs. Anything else -- a variable,
// `"DEL" + "ETE"`, strings.ToUpper("delete") -- fails this, deliberately: a
// literal is the only form a reader can verify by looking at the call site,
// and the argument's SHAPE is exactly what the concatenation and
// runtime-uppercasing evasions were built to defeat in an argument-agnostic
// check.
func isAllowedVerbCall(call *ast.CallExpr) bool {
	if len(call.Args) != 1 {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return false
	}
	return allowedVerbArgs[strings.ToUpper(v)]
}

// rooted reports whether e's value traces back to one of Typed, Dynamic,
// Discovery or Metadata -- through any chain of calls and selectors, or
// through a variable in rv assigned from one of them.
func rooted(e ast.Expr, rv map[string]bool) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return rv[v.Name]
	case *ast.SelectorExpr:
		if clientFields[v.Sel.Name] {
			return true
		}
		return rooted(v.X, rv)
	case *ast.CallExpr:
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		return rooted(sel.X, rv)
	case *ast.ParenExpr:
		return rooted(v.X, rv)
	case *ast.StarExpr:
		return rooted(v.X, rv)
	default:
		return false
	}
}

// rootedVars finds every local variable in body directly assigned -- by
// :=, plain =, or a var declaration's initializer -- from a rooted
// expression, so `typed := c.Typed; typed.CoreV1()...` is caught through
// typed exactly as c.Typed.CoreV1()... is caught directly. It runs to a
// fixed point (a handful of passes) so a second-order alias (`a := c.Typed;
// b := a`) is found regardless of which order the two assignments happen to
// appear in the source.
func rootedVars(body *ast.BlockStmt) map[string]bool {
	rv := map[string]bool{}
	for pass := 0; pass < 5; pass++ {
		changed := false
		ast.Inspect(body, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.AssignStmt:
				if len(s.Lhs) != len(s.Rhs) {
					return true
				}
				for i, lhs := range s.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok || id.Name == "_" || rv[id.Name] {
						continue
					}
					if rooted(s.Rhs[i], rv) {
						rv[id.Name] = true
						changed = true
					}
				}
			case *ast.ValueSpec:
				for i, name := range s.Names {
					if i >= len(s.Values) || rv[name.Name] {
						continue
					}
					if rooted(s.Values[i], rv) {
						rv[name.Name] = true
						changed = true
					}
				}
			}
			return true
		})
		if !changed {
			break
		}
	}
	return rv
}

// buildParents maps every node under root to its immediate parent, using
// the push-on-enter/pop-on-nil pattern ast.Inspect's own doc comment
// describes (f is called with nil once a node's children are all visited).
// go/ast hands back no parent pointers on its own; this is the smallest way
// to get them for the one thing this test needs them for -- telling a
// navigational call in the middle of a chain apart from the chain's actual
// final call, and telling a called selector apart from a bare method value.
func buildParents(root ast.Node) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	var stack []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		if len(stack) > 0 {
			parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})
	return parents
}

// Package nologsecrets enforces the invariant that caller IDs and extension
// PINs never reach the logs.
//
// CONTRIBUTING.md lists this as a hard rule, and CLAUDE.md notes that the
// invariants which matter are the ones that "fail in ways that look like
// working software". A PIN in a log line is exactly that: the phone keeps
// answering, every test stays green, and a credential for the house quietly
// accumulates in journald where any backup or log shipper can pick it up.
// Code review is the only thing standing between that and production today,
// and code review gets tired.
//
// The rule is not "never mention a secret near a logger" — that would be
// unusable. Logging how MANY digits arrived is genuinely useful and appears
// in session.go today, as does logging the last four of a number. So a
// sensitive value is allowed through when it passes an approved narrowing
// function first: len(), or a redactor like tail().
package nologsecrets

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

const Doc = `check that caller IDs and PINs never reach the logs

Reports any caller ID or extension PIN passed to an slog method. Wrap the
value in len() to log its size, or a redactor such as tail() to log a
non-identifying fragment.`

var Analyzer = &analysis.Analyzer{
	Name:     "nologsecrets",
	Doc:      Doc,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

// sensitive matches identifier and field names that hold a credential or a
// caller identity. Substring match, lowercased: this should catch pin,
// PIN, ext.PIN, enteredPin, callerE164, s.callerRaw.
var sensitive = []string{
	"pin",
	"digit",
	"entered",
	"caller", // covers callerE164, callerRaw, callerDisplay, callerNumber
	"secret",
	"password",
	"passwd",
	"token",
}

// narrowing names functions whose result is safe to log even when their input
// is not, because the output cannot identify the caller or be replayed as a
// credential. Keep this list short and obvious — every addition widens what
// can reach a log line.
var narrowing = []string{
	"len",    // how many digits arrived, never which
	"tail",   // policy.tail: last few characters only
	"redact", // policy.Redact and policy.RedactCaller
	"mask",
}

// slogMethods are the logging entry points. Value and pointer receivers on
// slog.Logger, plus the package-level functions.
//
// With and WithGroup are in here and it matters: attributes bound with With
// ride on *every subsequent line* from that logger, so a caller ID smuggled in
// there leaks far more than one that reaches a single Info call. This analyzer
// originally checked only the emit methods, and that gap is exactly how a full
// E.164 came to be attached to every session log line.
var slogMethods = map[string]bool{
	"Debug": true, "Info": true, "Warn": true, "Error": true, "Log": true,
	"DebugContext": true, "InfoContext": true, "WarnContext": true,
	"ErrorContext": true, "LogAttrs": true,
	"With": true, "WithGroup": true,
}

func run(pass *analysis.Pass) (any, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	insp.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node) {
		call := n.(*ast.CallExpr)
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !slogMethods[sel.Sel.Name] {
			return
		}
		if !isSlogReceiver(pass, sel.X) {
			return
		}
		// Skip the message argument; it is a literal in every call here, and
		// a non-literal message is a separate lint's problem.
		for _, arg := range call.Args[min(1, len(call.Args)):] {
			if name, bad := findSensitive(pass, arg); bad {
				pass.Reportf(arg.Pos(),
					"%s reaches the logs; wrap it in len() to log the count, or tail() to log a redacted fragment",
					name)
			}
		}
	})
	return nil, nil
}

// isSlogReceiver reports whether expr has type slog.Logger or *slog.Logger.
// Type-based rather than name-based so a logger named anything at all is
// still caught, and a non-logger that happens to have an Info method is not.
func isSlogReceiver(pass *analysis.Pass, expr ast.Expr) bool {
	tv, ok := pass.TypesInfo.Types[expr]
	if !ok {
		// A package qualifier such as slog.Info has no type here.
		if id, ok := expr.(*ast.Ident); ok {
			return id.Name == "slog"
		}
		return false
	}
	t := tv.Type
	if ptr, ok := t.Underlying().(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == "Logger" &&
		obj.Pkg() != nil && obj.Pkg().Path() == "log/slog"
}

// findSensitive walks an argument expression looking for a sensitive value
// that has not been narrowed. It returns the offending name.
func findSensitive(pass *analysis.Pass, expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		if isSensitive(pass, e.Name, e) {
			return e.Name, true
		}
	case *ast.SelectorExpr:
		// Check the field and the receiver: ext.PIN and s.callerE164 both
		// matter, and so does pin.String().
		if isSensitive(pass, e.Sel.Name, e) {
			return exprName(e), true
		}
		return findSensitive(pass, e.X)
	case *ast.CallExpr:
		// A narrowing call makes its arguments safe. Everything else — a
		// fmt.Sprintf, a string conversion — passes the value through.
		if isNarrowing(e.Fun) {
			return "", false
		}
		for _, a := range e.Args {
			if n, bad := findSensitive(pass, a); bad {
				return n, true
			}
		}
		return findSensitive(pass, e.Fun)
	case *ast.StarExpr:
		return findSensitive(pass, e.X)
	case *ast.ParenExpr:
		return findSensitive(pass, e.X)
	case *ast.UnaryExpr:
		return findSensitive(pass, e.X)
	case *ast.BinaryExpr:
		if n, bad := findSensitive(pass, e.X); bad {
			return n, true
		}
		return findSensitive(pass, e.Y)
	case *ast.IndexExpr:
		return findSensitive(pass, e.X)
	case *ast.SliceExpr:
		// digits[:2] is still digits. Slicing is not redaction.
		return findSensitive(pass, e.X)
	}
	return "", false
}

// isSensitive combines the name heuristic with the value's type. Name alone
// is too blunt: PinLength contains "pin" but holds 6, and logging how long a
// PIN is leaks nothing. A credential or a phone number is textual, so only
// string-shaped values can carry one out.
func isSensitive(pass *analysis.Pass, name string, expr ast.Expr) bool {
	return isSensitiveName(name) && isTextual(pass, expr)
}

// isTextual reports whether expr could hold text. Unknown types count as
// textual: this is a security lint, so an unresolved type should fail loud
// rather than pass silently.
func isTextual(pass *analysis.Pass, expr ast.Expr) bool {
	tv, ok := pass.TypesInfo.Types[expr]
	if !ok || tv.Type == nil {
		return true
	}
	switch t := tv.Type.Underlying().(type) {
	case *types.Basic:
		return t.Info()&types.IsString != 0
	case *types.Slice:
		// []byte and []rune are text in a thin disguise.
		if b, ok := t.Elem().Underlying().(*types.Basic); ok {
			return b.Kind() == types.Byte || b.Kind() == types.Rune
		}
		return false
	case *types.Interface:
		// any / fmt.Stringer could be anything, including a PIN.
		return true
	}
	return false
}

func isSensitiveName(name string) bool {
	l := strings.ToLower(name)
	for _, s := range sensitive {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func isNarrowing(fun ast.Expr) bool {
	name := ""
	switch f := fun.(type) {
	case *ast.Ident:
		name = f.Name
	case *ast.SelectorExpr:
		name = f.Sel.Name
	default:
		return false
	}
	l := strings.ToLower(name)
	for _, n := range narrowing {
		if strings.Contains(l, n) {
			return true
		}
	}
	return false
}

func exprName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return fmt.Sprintf("%s.%s", exprName(v.X), v.Sel.Name)
	}
	return "value"
}

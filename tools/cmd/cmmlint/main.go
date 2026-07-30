// cmmlint runs the static analysis that `go vet` does not.
//
// `go vet` already runs 35 analyzers from golang.org/x/tools/go/analysis —
// including the ones that matter most for goroutine-per-call code
// (lostcancel, copylocks, atomic, waitgroup, testinggoroutine). This binary
// adds the passes that ship in x/tools but are not in vet's default set,
// plus the project's own invariant check.
//
// Usage:
//
//	go run ./tools/cmd/cmmlint ./...      # from the repo root
//	make lint
//
// It is also a valid vettool, so `go vet -vettool=$(which cmmlint) ./...`
// works if you would rather drive it that way.
package main

import (
	"golang.org/x/tools/go/analysis/multichecker"
	"golang.org/x/tools/go/analysis/passes/nilness"

	"callmemaybe/tools/nologsecrets"
)

func main() {
	multichecker.Main(
		// nilness: nil dereferences and impossible nil comparisons. Not in
		// vet's default set because it needs SSA, which costs time vet does
		// not want to spend on every build.
		nilness.Analyzer,

		// The house rule: caller IDs and PINs never reach the logs.
		nologsecrets.Analyzer,
	)
}

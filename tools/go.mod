// Lint tooling lives in its own module on purpose.
//
// The daemon ships as one static binary with exactly two dependencies, and
// that is a claim worth protecting: `go build ./...` in the root module never
// sees golang.org/x/tools, because Go excludes nested modules from the parent
// module's package patterns. Nothing here is linked into doorman.
module callmemaybe/tools

go 1.26.8

require golang.org/x/tools v0.49.0

require (
	golang.org/x/mod v0.40.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
)

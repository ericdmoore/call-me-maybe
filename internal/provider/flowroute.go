package provider

// Flowroute, which is here to not implement a capability.
//
// It looks like an empty file and it is the reason the optional interface is
// worth having. Without an invoiced provider in the map, "balance is a
// capability" is a claim rather than a shape: every entry would implement
// [Balance], the type assertion would always succeed, and the first postpaid
// provider somebody added would discover that the CLI has nowhere to put the
// answer "there is no balance".
//
// With it, `doorman balance` says "Flowroute is postpaid — no balance to
// report", which is a fact about the account rather than a zero that looks
// alarming or a blank that looks broken. That sentence is the milestone.
//
// The billing model is not a guess: site/src/data/providers.ts has published
// Flowroute as postpaid since the provider comparison went up, listing
// "postpaid, so a zero balance cannot silently take the line down" as a
// reason to choose them. This is the same fact, in the place the CLI can read
// it. A provider whose model is genuinely mixed — Telnyx and CallCentric are
// both "either" on that page — is deliberately absent instead of guessed at.

type flowroute struct{}

func newFlowroute(Config) (Provider, error) { return flowroute{}, nil }

func (flowroute) Name() string     { return "flowroute" }
func (flowroute) Billing() Billing { return Postpaid }

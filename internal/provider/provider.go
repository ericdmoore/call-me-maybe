// Package provider is what doorman knows about the companies that carry its
// calls. Today that is one question: how much credit is left.
//
// A prepaid trunk that reaches zero does not error. Inbound calls simply stop
// arriving, and "nobody called today" is indistinguishable from a quiet
// Tuesday — no log line, no alarm, and no symptom at all until somebody
// mentions they tried to reach you last week. With several ventures on one box
// it is every business's inbound at once, and they find out from a customer.
//
// # Balance is a capability, not a provider feature
//
// The optional-interface shape internal/voice already uses for Lister and
// Estimator. Every provider satisfies [Provider], which says almost nothing;
// what a provider can actually *do* is discovered by asking for a capability:
//
//	if b, ok := p.(provider.Balance); ok { ... }
//
// A prepaid provider implements [Balance]. An invoiced one does not, and its
// [Provider.Billing] is what turns that absence into an answer — "Flowroute is
// postpaid, there is no balance to report" — rather than a zero that looks
// alarming or a blank that looks broken. That distinction is the whole point:
// without it this would be a VoIP.ms feature wearing a general name.
//
// # Nothing here runs on a call path, and the daemon never reads it
//
// A provider's API credential usually manages DIDs, sub-accounts and billing:
// considerably more privilege than the SIP sub-account password, which
// docs/RUNBOOK.md §1 already says to keep off the Pi. So this package is
// reached from `doorman balance` and from nowhere else. The daemon does not
// import it, does not resolve the credential, and does not poll — which keeps
// the higher-privilege key off the call path and out of the long-running
// process entirely. The recommended arrangement is to run the check wherever
// alerting already lives, against a copy of trunks.toml, and never on the Pi.
//
// # The URL is a credential
//
// The one API here takes its credentials as query parameters, so the request
// URL carries a password and must never be printed. net/http wraps every
// transport failure in a *url.Error holding the whole URL, which no name-based
// analyzer can catch because by then the identifier is `err`. [scrub] unwraps
// it and keeps the host alone, exactly as internal/notify does with the
// webhook URL.
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A Provider is one company, as much of it as doorman needs to know. The
// interface is nearly empty on purpose — it exists to be type-asserted away
// from, not to be implemented against.
type Provider interface {
	// Name is the canonical provider name: the key in the backend map, and
	// what a trunks.toml `provider` field resolves to however it was spelled.
	Name() string
	// Billing is how the account is paid for. It is what makes a missing
	// capability an answer instead of a gap.
	Billing() Billing
}

// A Balance reports what is left on a prepaid account. Optional: a provider
// that invoices simply does not implement it.
type Balance interface {
	Balance(ctx context.Context) (Amount, error)
}

// Billing is how an account is paid for, which decides whether "no balance" is
// a fact or a failure.
type Billing string

const (
	// Prepaid accounts hold credit and can run out. These are the ones worth
	// watching: reaching zero stops inbound with no error anywhere.
	Prepaid Billing = "prepaid"
	// Postpaid accounts are invoiced. There is no balance to run out, so
	// nothing here can be low and nothing should be printed as though it were.
	Postpaid Billing = "postpaid"
)

// Amount is money, for display and for comparison against a threshold — never
// for arithmetic. A float is the right shape for "is this under ten dollars";
// it would be the wrong shape for a ledger, and this is not one.
type Amount struct {
	Value float64
	// Currency is ISO 4217 when the provider says which, and empty when it
	// does not. VoIP.ms is the second kind: an account is held in USD or CAD
	// and getBalance reports the number without saying which, so the honest
	// answer is to print the number and not invent a symbol for it.
	Currency string
}

// String renders an amount to two decimal places, with the currency only when
// the provider actually named one.
func (a Amount) String() string {
	s := strconv.FormatFloat(a.Value, 'f', 2, 64)
	if a.Currency == "" {
		return s
	}
	return s + " " + a.Currency
}

// Config carries what any backend might need. Fields not relevant to the
// chosen provider are ignored.
type Config struct {
	// Username is the API login. For VoIP.ms it is the account email address,
	// which is not the SIP sub-account and not interchangeable with it.
	Username string
	// Password is the API password, resolved by the caller from the .env
	// variable trunks.toml names. It is never read from the environment here:
	// one place resolves secrets, and it is the one the operator ran.
	Password string
	// HTTP is the client for network backends. Nil means a sensible default.
	HTTP *http.Client
	// Endpoint overrides the service URL, for testing. Nothing in production
	// sets it — a flag that repoints a billing API at another host is a
	// foot-gun, so this stays a field rather than becoming one.
	Endpoint string
}

func (c Config) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	// Short: this runs from cron with a person's attention nowhere near it,
	// and a provider that is not answering is itself the answer.
	return &http.Client{Timeout: 20 * time.Second}
}

// factory builds one provider client.
type factory func(Config) (Provider, error)

// backends is an explicit map rather than init()-time self-registration, for
// the same reason internal/voice's is: the list you can grep beats indirection
// that hides it. Both entries are load-bearing — one implements the capability
// and one deliberately does not, which is what keeps the optional interface
// honest rather than theoretical.
var backends = map[string]factory{
	"voip.ms":   newVoIPMS,
	"flowroute": newFlowroute,
}

// Backends lists the providers doorman has a client for, sorted.
func Backends() []string {
	names := make([]string, 0, len(backends))
	for n := range backends {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// BalanceBackends lists the providers doorman can actually report a balance
// for — the list somebody whose provider is not recognised wants to see, and a
// strict subset of [Backends].
//
// Discovered by building each client and asking, rather than declared beside
// the map. A second list is a list that can disagree with the first, and the
// way that failure shows up is a CLI advertising a capability it does not
// have. The credentials here are a sentinel: constructors do no I/O, and the
// only thing they are used for is getting past a refusal to build without any.
func BalanceBackends() []string {
	var names []string
	for name, build := range backends {
		p, err := build(Config{Username: "probe", Password: "probe"})
		if err != nil {
			continue
		}
		if _, ok := p.(Balance); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// flatten reduces a provider name to the form two spellings of it share:
// lowercase, without the punctuation people disagree about. The `provider`
// field was documented as display-only and has acquired a second job here, so
// refusing "voipms" because the example writes "voip.ms" would be a
// configuration error over a full stop.
func flatten(name string) string {
	return strings.NewReplacer(" ", "", "-", "", ".", "", "_", "").
		Replace(strings.ToLower(strings.TrimSpace(name)))
}

// canonicalByFlat resolves any spelling to the one this package prints.
var canonicalByFlat = func() map[string]string {
	m := make(map[string]string, len(backends))
	for name := range backends {
		m[flatten(name)] = name
	}
	return m
}()

// Canonical resolves however a provider was spelled to the name this package
// knows it by, or "" when it knows no such provider.
func Canonical(name string) string { return canonicalByFlat[flatten(name)] }

// ErrUnknown means doorman has no client for this provider. Deliberately not
// "this provider has no balance": those are different answers with different
// fixes, and a caller that conflates them tells an operator their prepaid
// account is postpaid.
var ErrUnknown = errors.New("provider: no client for this provider")

// ErrNoCredentials is returned when a provider that needs credentials has
// none, so the caller can name the variable to set rather than letting the
// vendor answer with an authentication failure.
var ErrNoCredentials = errors.New("provider: no credentials")

// New builds a client for a provider name, however it was spelled.
func New(name string, cfg Config) (Provider, error) {
	canonical := Canonical(name)
	if canonical == "" {
		return nil, fmt.Errorf("%w %q (have %s)", ErrUnknown, name, strings.Join(Backends(), ", "))
	}
	return backends[canonical](cfg)
}

// scrub reduces a transport error to its cause and the host it was talking to.
//
// The credentials travel in the query string — the API this package talks to
// takes them no other way — so the whole URL is a credential. net/http wraps
// every transport failure in a *url.Error carrying it, and no name-based
// analyzer will ever catch that: by the time it is a problem the identifier is
// `err`. internal/notify unwraps exactly this way for exactly this reason, and
// this is the second customer for the rule.
func scrub(err error, host string) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		err = uerr.Err
	}
	return fmt.Errorf("%s: %w", host, err)
}

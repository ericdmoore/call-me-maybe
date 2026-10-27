package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"callmemaybe/internal/policy"
)

// policy refuses a trunk whose API credentials are not the shape its provider
// takes, from a table of its own because nothing under internal/ may import
// this package. This holds the table to the clients: each is built with only a
// login and with only a key, and the shape it accepts is the one the table
// must name. A client that takes neither, or both, is one the table leaves
// unchecked.
func TestPolicyKnowsWhichCredentialsEachClientTakes(t *testing.T) {
	for _, name := range Backends() {
		_, loginErr := New(name, Config{Username: "a@example.invalid", Password: "x"})
		_, keyErr := New(name, Config{Key: "KEY-fiction"})
		login := !errors.Is(loginErr, ErrNoCredentials)
		key := !errors.Is(keyErr, ErrNoCredentials)
		want := policy.APIAuthUnknown
		switch {
		case login && !key:
			want = policy.APIAuthLogin
		case key && !login:
			want = policy.APIAuthKey
		}
		if got := policy.ProviderAPIAuth(name); got != want {
			t.Errorf("%s: policy.ProviderAPIAuth = %v, but the client accepts login=%v key=%v",
				name, got, login, key)
		}
	}
}

func TestBalanceIsOptional(t *testing.T) {
	prepaid, err := New("voip.ms", Config{Username: "a@example.invalid", Password: "x"})
	if err != nil {
		t.Fatalf("New voip.ms: %v", err)
	}
	if _, ok := prepaid.(Balance); !ok {
		t.Error("voip.ms does not implement Balance — the one provider that must")
	}

	postpaid, err := New("flowroute", Config{})
	if err != nil {
		t.Fatalf("New flowroute: %v", err)
	}
	if _, ok := postpaid.(Balance); ok {
		t.Error("flowroute implements Balance — the whole point of the optional " +
			"interface is that an invoiced provider does not")
	}
	// And that the absence is an answer rather than a gap: without this the CLI
	// has nothing to print but a zero or a blank.
	if postpaid.Billing() != Postpaid {
		t.Errorf("flowroute billing = %q, want %q", postpaid.Billing(), Postpaid)
	}
}

func TestCanonicalAcceptsTheSpellingsPeopleWrite(t *testing.T) {
	for _, spelled := range []string{"voip.ms", "VoIP.ms", "voipms", "VOIP-MS", " voip_ms "} {
		if got := Canonical(spelled); got != "voip.ms" {
			t.Errorf("Canonical(%q) = %q, want voip.ms", spelled, got)
		}
	}
	if got := Canonical("callcentric"); got != "" {
		t.Errorf("Canonical(callcentric) = %q, want empty — doorman has no client for it", got)
	}
}

func TestNewNamesWhatItDoesHaveWhenItDoesNotHaveThis(t *testing.T) {
	_, err := New("callcentric", Config{})
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("New(callcentric) error = %v, want ErrUnknown", err)
	}
	// The list matters: "unknown provider" with nothing else leaves an operator
	// guessing whether they misspelled it.
	for _, want := range Backends() {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name the %q backend: %v", want, err)
		}
	}
}

// A provider that needs credentials and has none must say so rather than let
// the vendor answer with an authentication failure that reads like a typo.
func TestNewRefusesMissingCredentials(t *testing.T) {
	for _, cfg := range []Config{{}, {Username: "a@example.invalid"}, {Password: "x"}} {
		if _, err := New("voip.ms", cfg); !errors.Is(err, ErrNoCredentials) {
			t.Errorf("New(voip.ms, %+v) error = %v, want ErrNoCredentials", cfg, err)
		}
	}
}

func TestAmountPrintsTwoPlacesAndOnlyARealCurrency(t *testing.T) {
	if got := (Amount{Value: 12.3456}).String(); got != "12.35" {
		t.Errorf("Amount.String() = %q, want 12.35", got)
	}
	if got := (Amount{Value: 12.3456, Currency: "USD"}).String(); got != "12.35 USD" {
		t.Errorf("Amount.String() = %q, want \"12.35 USD\"", got)
	}
	if got := (Amount{Value: -3}).String(); got != "-3.00" {
		t.Errorf("a negative balance is a real state: %q", got)
	}
}

// The list somebody with an unrecognised provider is shown, and it must not
// advertise a provider doorman cannot actually ask.
func TestBalanceBackendsAreOnlyTheOnesThatCanAnswer(t *testing.T) {
	got := BalanceBackends()
	if strings.Join(got, ",") != "telnyx,voip.ms" {
		t.Fatalf("BalanceBackends() = %v, want [telnyx voip.ms]", got)
	}
	for _, name := range got {
		if Canonical(name) == "" {
			t.Errorf("%q is advertised but is not a known provider", name)
		}
	}
	if len(BalanceBackends()) >= len(Backends()) {
		t.Error("every backend reports a balance, which means the optional " +
			"interface is not being exercised by anything")
	}
}

// The second capability, held to the same rule: advertised only where it is
// real, and not by every backend, or the optional interface proves nothing.
func TestNumberBackendsAreOnlyTheOnesThatCanAnswer(t *testing.T) {
	got := NumberBackends()
	if strings.Join(got, ",") != "telnyx" {
		t.Fatalf("NumberBackends() = %v, want [telnyx]", got)
	}
	voipms, err := New("voip.ms", Config{Username: "a@example.invalid", Password: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := voipms.(NumberLister); ok {
		t.Error("voip.ms claims NumberLister — nothing here asks its API for DIDs yet, " +
			"and advertising it would make every policy number read as missing")
	}
}

func TestAnUnassignedConnectionIsTheZeroValue(t *testing.T) {
	if (Connection{}).Assigned() {
		t.Error("the zero Connection must read as assigned to none")
	}
	if !(Connection{Name: "house"}).Assigned() || !(Connection{ID: "1"}).Assigned() {
		t.Error("a connection with an id or a name is assigned")
	}
}

func TestBackendsAreListedSorted(t *testing.T) {
	got := Backends()
	if len(got) < 2 {
		t.Fatalf("expected at least a prepaid and an invoiced backend, got %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("Backends() is not sorted: %v", got)
		}
	}
}

// The interface is what the plan specifies, checked structurally so a
// well-meaning refactor cannot quietly change the shape every future provider
// will be written against.
func TestTheCapabilityHasTheShapeThePlanSpecifies(t *testing.T) {
	var b Balance = stubBalance{}
	if _, err := b.Balance(context.Background()); err != nil {
		t.Fatalf("Balance(ctx) (Amount, error): %v", err)
	}
	var p Provider = stubBalance{}
	if p.Name() == "" {
		t.Error("a provider with no name cannot be reported against a trunk")
	}
}

type stubBalance struct{}

func (stubBalance) Name() string     { return "stub" }
func (stubBalance) Billing() Billing { return Prepaid }
func (stubBalance) Balance(context.Context) (Amount, error) {
	return Amount{Value: 1}, nil
}

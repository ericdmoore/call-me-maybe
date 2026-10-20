package backup

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"filippo.io/age"
)

// All material is synthetic and generated in the test. Never commit a customer's
// identity, recovery phrase, envelope or backup as a fixture.
func TestPrintedFactorRecoveryIdentity(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	scope := RecoveryScope{"synthetic-account", "synthetic-house", "generation-one"}
	phrase := "synthetic recovery phrase used only by this test"
	sealed, err := SealRecoveryIdentity(identity, scope, phrase)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(identity.String())) || bytes.Contains(sealed, []byte(phrase)) || bytes.Contains(sealed, []byte(scope.House)) {
		t.Fatal("plaintext in envelope")
	}
	opened, err := OpenRecoveryIdentity(sealed, scope, phrase)
	if err != nil || opened.Recipient().String() != identity.Recipient().String() {
		t.Fatalf("identity round trip failed: %v", err)
	}
	for _, wrong := range []RecoveryScope{{"other", scope.House, scope.Generation}, {scope.Account, "other", scope.Generation}, {scope.Account, scope.House, "other"}} {
		key, err := OpenRecoveryIdentity(sealed, wrong, phrase)
		if key != nil || !errors.Is(err, ErrRecoveryScope) {
			t.Fatalf("accepted foreign scope: %v", err)
		}
	}
	for _, input := range [][]byte{nil, sealed[:len(sealed)-1], append(append([]byte{}, sealed...), 0)} {
		if key, err := OpenRecoveryIdentity(input, scope, phrase); key != nil || !errors.Is(err, ErrRecoveryEnvelope) {
			t.Fatalf("accepted malformed envelope: %v", err)
		}
	}
	tampered := append([]byte{}, sealed...)
	tampered[len(tampered)-1] ^= 1
	if key, err := OpenRecoveryIdentity(tampered, scope, phrase); key != nil || !errors.Is(err, ErrRecoveryEnvelope) {
		t.Fatalf("accepted tampering: %v", err)
	}
	if key, err := OpenRecoveryIdentity(sealed, scope, phrase+" wrong"); key != nil || !errors.Is(err, ErrRecoveryEnvelope) {
		t.Fatalf("accepted wrong factor: %v", err)
	}
	// Preparing a new generation leaves the old one usable until the caller
	// confirms the new sheet and the service commits that generation transition.
	next := scope
	next.Generation = "generation-two"
	newPhrase := phrase + " new"
	prepared, err := SealRecoveryIdentity(identity, next, newPhrase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRecoveryIdentity(sealed, scope, phrase); err != nil {
		t.Fatal("preparing rotation invalidated old factor")
	}
	if _, err := OpenRecoveryIdentity(prepared, next, newPhrase); err != nil {
		t.Fatal("new generation cannot be confirmed")
	}
	if _, err := OpenRecoveryIdentity(prepared, next, phrase); !errors.Is(err, ErrRecoveryEnvelope) {
		t.Fatal("old factor opened new generation")
	}
}

func TestRecoveryIdentityRejectsInvalidInputs(t *testing.T) {
	identity, _ := age.GenerateX25519Identity()
	scope := RecoveryScope{"a", "h", "g"}
	for _, invalid := range []RecoveryScope{{}, {"a", "", "g"}, {"a", "h", ""}} {
		if _, err := SealRecoveryIdentity(identity, invalid, "synthetic"); err == nil {
			t.Fatal("missing scope accepted")
		}
		if _, err := OpenRecoveryIdentity(nil, invalid, "synthetic"); err == nil {
			t.Fatal("missing expected scope accepted")
		}
	}
	if _, err := SealRecoveryIdentity(nil, scope, "synthetic"); err == nil {
		t.Fatal("nil identity")
	}
	if _, err := SealRecoveryIdentity(identity, scope, " "); err == nil {
		t.Fatal("blank phrase")
	}
	if _, err := OpenRecoveryIdentity(nil, scope, " "); err == nil {
		t.Fatal("blank phrase")
	}
	if _, err := OpenRecoveryIdentity(make([]byte, maxRecoveryEnvelope+1), scope, "synthetic"); err == nil {
		t.Fatal("oversized envelope")
	}
	if _, err := SealRecoveryIdentity(identity, RecoveryScope{strings.Repeat("a", maxRecoveryPayload), "h", "g"}, "synthetic"); err == nil {
		t.Fatal("oversized payload")
	}
}

func TestRecoveryIdentityRejectsAuthenticatedMalformedPayload(t *testing.T) {
	scope := RecoveryScope{"a", "h", "g"}
	phrase := "synthetic test phrase"
	// Use the standard age encoder so these exercise payload validation after
	// successful authentication, rather than failing at the encryption boundary.
	seal := func(plain []byte) []byte {
		t.Helper()
		r, err := age.NewScryptRecipient(phrase)
		if err != nil {
			t.Fatal(err)
		}
		r.SetWorkFactor(1) // fast synthetic fixture; readers accept up to the production ceiling
		var b bytes.Buffer
		w, err := age.Encrypt(&b, r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(plain); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	for _, plain := range []string{
		"not JSON",
		`{"format":"unknown","scope":{"account":"a","house":"h","generation":"g"},"identity":"bad"}`,
		`{"format":"doorman-recovery-identity-v0","scope":{"account":"a","house":"h","generation":"g"},"identity":"bad","extra":1}`,
		`{"format":"doorman-recovery-identity-v0","scope":{"account":"a","house":"h","generation":"g"},"identity":"bad"} {}`,
		`{"format":"doorman-recovery-identity-v0","scope":{"account":"a","house":"h","generation":"g"},"identity":"bad"}`,
		strings.Repeat("x", maxRecoveryPayload+1),
	} {
		if key, err := OpenRecoveryIdentity(seal([]byte(plain)), scope, phrase); key != nil || !errors.Is(err, ErrRecoveryEnvelope) {
			t.Fatalf("accepted malformed plaintext: %v", err)
		}
	}
}

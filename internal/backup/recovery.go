package backup

// Experimental recovery payload for the printed-factor integration (#80).
// This is not a released disk/wire format or an exposed CLI command. Each factor
// gets its own age envelope over the same generation-bound payload: native age
// scrypt cannot coexist with another recipient type in one file. Factor adapters
// therefore do not require changing the encrypted identity payload later.
import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"filippo.io/age"
)

const recoveryFormat = "doorman-recovery-identity-v0"
const maxRecoveryEnvelope = 64 * 1024
const maxRecoveryPayload = 4096
const recoveryWorkFactor = 18

var ErrRecoveryEnvelope = errors.New("recovery envelope cannot be opened")
var ErrRecoveryScope = errors.New("recovery envelope belongs to a different scope")

// RecoveryScope comes from a separately authorized recovery response, not from
// the ciphertext being opened. A destination must compare all three fields.
type RecoveryScope struct {
	Account    string `json:"account"`
	House      string `json:"house"`
	Generation string `json:"generation"`
}

type recoveryPayload struct {
	Format   string        `json:"format"`
	Scope    RecoveryScope `json:"scope"`
	Identity string        `json:"identity"`
}

func validRecoveryScope(s RecoveryScope) bool {
	return s.Account != "" && s.House != "" && s.Generation != ""
}

// SealRecoveryIdentity wraps an existing identity without rotating the backup
// recipient. The caller must supply a cryptographically generated, customer-held
// phrase; this primitive does not assess the entropy of human-chosen passwords.
// Neither phrase nor identity may be logged or sent to the hosted service.
func SealRecoveryIdentity(identity *age.X25519Identity, scope RecoveryScope, phrase string) ([]byte, error) {
	if identity == nil || !validRecoveryScope(scope) || strings.TrimSpace(phrase) == "" {
		return nil, ErrRecoveryEnvelope
	}
	plain, err := json.Marshal(recoveryPayload{recoveryFormat, scope, identity.String()})
	if err != nil || len(plain) > maxRecoveryPayload {
		return nil, ErrRecoveryEnvelope
	}
	recipient, err := age.NewScryptRecipient(phrase)
	if err != nil {
		return nil, ErrRecoveryEnvelope
	}
	recipient.SetWorkFactor(recoveryWorkFactor)
	var encrypted bytes.Buffer
	writer, err := age.Encrypt(&encrypted, recipient)
	if err != nil {
		return nil, ErrRecoveryEnvelope
	}
	if _, err := writer.Write(plain); err != nil {
		return nil, ErrRecoveryEnvelope
	}
	if err := writer.Close(); err != nil {
		return nil, ErrRecoveryEnvelope
	}
	return encrypted.Bytes(), nil
}

// OpenRecoveryIdentity authenticates the entire encrypted payload before returning
// a key. Size and scrypt-work limits bound resources for attacker-supplied files.
// Every cryptographic/parser error is deliberately secret-free and indistinct.
func OpenRecoveryIdentity(encrypted []byte, expected RecoveryScope, phrase string) (*age.X25519Identity, error) {
	if !validRecoveryScope(expected) || strings.TrimSpace(phrase) == "" || len(encrypted) > maxRecoveryEnvelope {
		return nil, ErrRecoveryEnvelope
	}
	factor, err := age.NewScryptIdentity(phrase)
	if err != nil {
		return nil, ErrRecoveryEnvelope
	}
	factor.SetMaxWorkFactor(recoveryWorkFactor)
	reader, err := age.Decrypt(bytes.NewReader(encrypted), factor)
	if err != nil {
		return nil, ErrRecoveryEnvelope
	}
	plain, err := io.ReadAll(io.LimitReader(reader, maxRecoveryPayload+1))
	if err != nil || len(plain) > maxRecoveryPayload {
		return nil, ErrRecoveryEnvelope
	}
	var payload recoveryPayload
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || payload.Format != recoveryFormat {
		return nil, ErrRecoveryEnvelope
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, ErrRecoveryEnvelope
	}
	if payload.Scope != expected {
		return nil, ErrRecoveryScope
	}
	identity, err := age.ParseX25519Identity(payload.Identity)
	if err != nil {
		return nil, ErrRecoveryEnvelope
	}
	return identity, nil
}

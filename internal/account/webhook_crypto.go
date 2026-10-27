package account

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strconv"

	"callmemaybe/internal/syncwire"
	"callmemaybe/internal/webhookconfig"
)

const webhookScheme = "doorman.webhook.v1"

// AssociatedData binds ciphertext to this enrolment and this exact proposal.
func webhookAssociatedData(box string, item syncwire.QueueItem) []byte {
	data, _ := json.Marshal([]string{webhookScheme, box, item.Base.Generation, item.Base.Revision, item.Id, strconv.FormatInt(item.NotAfter, 10)})
	return data
}
func decryptWebhook(private []byte, box string, item syncwire.QueueItem) (webhookconfig.Config, error) {
	if item.Verb == nil || item.Verbs != nil {
		return webhookconfig.Config{}, webhookconfig.ErrInvalid
	}
	v, err := item.Verb.AsVerb9()
	if err != nil || v.Name != "webhook.set" {
		return webhookconfig.Config{}, webhookconfig.ErrInvalid
	}
	decode := base64.RawURLEncoding.DecodeString
	ephemeral, e1 := decode(v.Envelope.EphemeralKey)
	nonce, e2 := decode(v.Envelope.Nonce)
	ciphertext, e3 := decode(v.Envelope.Ciphertext)
	key, e4 := ecdh.X25519().NewPrivateKey(private)
	peer, e5 := ecdh.X25519().NewPublicKey(ephemeral)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || len(nonce) != 12 || len(ciphertext) > 8192 {
		return webhookconfig.Config{}, webhookconfig.ErrInvalid
	}
	shared, err := key.ECDH(peer)
	if err != nil {
		return webhookconfig.Config{}, webhookconfig.ErrInvalid
	}
	aad := webhookAssociatedData(box, item)
	salt := sha256.Sum256(aad)
	derived, err := hkdf.Key(sha256.New, shared, salt[:], webhookScheme, 32)
	if err != nil {
		return webhookconfig.Config{}, webhookconfig.ErrInvalid
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		return webhookconfig.Config{}, webhookconfig.ErrInvalid
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return webhookconfig.Config{}, webhookconfig.ErrInvalid
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return webhookconfig.Config{}, webhookconfig.ErrInvalid
	}
	normal := webhookconfig.NormaliseJSON(plain)
	if normal == nil {
		return webhookconfig.Config{}, webhookconfig.ErrInvalid
	}
	var c webhookconfig.Config
	_ = json.Unmarshal(normal, &c)
	if c.Domain != v.Domain {
		return webhookconfig.Config{}, webhookconfig.ErrInvalid
	}
	return c, nil
}

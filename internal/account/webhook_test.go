package account

import (
	"callmemaybe/internal/syncwire"
	"callmemaybe/internal/webhookconfig"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestWebhookWebCryptoInteroperabilityAndBinding(t *testing.T) {
	data, err := os.ReadFile("testdata/webhook.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Box, PrivateKey string
		Config          webhookconfig.Config
		Item            syncwire.QueueItem
	}
	if json.Unmarshal(data, &vector) != nil {
		t.Fatal("fixture")
	}
	key, _ := base64.RawURLEncoding.DecodeString(vector.PrivateKey)
	got, err := decryptWebhook(key, vector.Box, vector.Item)
	if err != nil || got != vector.Config {
		t.Fatalf("WebCrypto envelope failed: %v", err)
	}
	for _, mutate := range []func(*syncwire.QueueItem){
		func(q *syncwire.QueueItem) { q.Id = "other" }, func(q *syncwire.QueueItem) { q.Base.Generation = "other" }, func(q *syncwire.QueueItem) { q.Base.Revision = "2" }, func(q *syncwire.QueueItem) { q.NotAfter++ },
		func(q *syncwire.QueueItem) {
			v, _ := q.Verb.AsVerb9()
			v.Domain = "https://other.example"
			_ = q.Verb.FromVerb9(v)
		},
		func(q *syncwire.QueueItem) {
			v, _ := q.Verb.AsVerb9()
			v.Envelope.Nonce = "bad"
			_ = q.Verb.FromVerb9(v)
		},
		func(q *syncwire.QueueItem) {
			v, _ := q.Verb.AsVerb9()
			v.Envelope.Ciphertext = "invalid"
			_ = q.Verb.FromVerb9(v)
		},
		func(q *syncwire.QueueItem) {
			v, _ := q.Verb.AsVerb9()
			v.Envelope.EphemeralKey = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
			_ = q.Verb.FromVerb9(v)
		},
		func(q *syncwire.QueueItem) { q.Verb = nil },
	} {
		var q syncwire.QueueItem
		raw, _ := json.Marshal(vector.Item)
		_ = json.Unmarshal(raw, &q)
		mutate(&q)
		if _, err := decryptWebhook(key, vector.Box, q); err == nil {
			t.Fatal("accepted altered proposal")
		}
	}
	if _, err := decryptWebhook(key, "other-box", vector.Item); err == nil {
		t.Fatal("wrong box")
	}
	if _, err := decryptWebhook([]byte("bad"), vector.Box, vector.Item); err == nil {
		t.Fatal("wrong key")
	}
	c := New(t.TempDir())
	seed(t, c)
	saveTestRecovery(t, c, func(s *state) { s.Box = vector.Box; s.TransportKey = vector.PrivateKey })
	if got, err := c.OpenWebhook(vector.Box, vector.Item); err != nil || got != vector.Config {
		t.Fatal(err)
	}
	saveTestRecovery(t, c, func(s *state) { s.RemovedAt = time.Now() })
	if _, err := c.OpenWebhook(vector.Box, vector.Item); err == nil {
		t.Fatal("removed enrolment decrypted configuration")
	}
}

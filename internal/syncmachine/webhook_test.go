package syncmachine

import (
	"bytes"
	"callmemaybe/internal/syncwire"
	"callmemaybe/internal/webhookconfig"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebhookPrivateRevisionAndActivation(t *testing.T) {
	m, p, _ := fixture(t)
	p.Webhook = filepath.Join(filepath.Dir(p.Policy), "webhook.toml")
	m.paths = p
	first, err := m.Observe()
	if err != nil || first.Configuration.Webhook == nil {
		t.Fatal(err)
	}
	q := queue(t, first, "")
	v := syncwire.Verb{}
	_ = v.FromVerb9(syncwire.Verb9{Name: "webhook.set", Domain: "https://receiver.example", Envelope: syncwire.WebhookEnvelope{EphemeralKey: strings.Repeat("A", 43), Nonce: strings.Repeat("A", 16), Ciphertext: "encrypted"}})
	q.Verb = &v
	prefix := "/secret-first"
	m.OpenWebhook = func(string, syncwire.QueueItem) (webhookconfig.Config, error) {
		return webhookconfig.Config{Domain: "https://receiver.example", PathPrefix: prefix}, nil
	}
	m.Applied = func(Paths, map[string][]byte) bool { return false }
	receipt, err := m.Apply(q)
	if err != nil || receipt.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("save %v %v", receipt, err)
	}
	data, _ := os.ReadFile(p.Webhook)
	info, _ := os.Stat(p.Webhook)
	if info.Mode().Perm() != 0600 || !bytes.Contains(data, []byte(prefix)) {
		t.Fatal("private file missing")
	}
	snapshot, _ := m.Latest()
	wire, _ := json.Marshal(snapshot)
	if bytes.Contains(wire, []byte(prefix)) || snapshot.Revision == first.Revision {
		t.Fatal("private projection or revision")
	}
	daemon := webhookconfig.OpenStore(p.Webhook)
	m.Applied = func(_ Paths, files map[string][]byte) bool {
		return daemon.Current().Hash == webhookconfig.Hash(files[p.Webhook])
	}
	receipt, err = m.Apply(q)
	if err != nil || receipt.Status != syncwire.ReceiptStatusApplied {
		t.Fatalf("activation %v %v", receipt, err)
	}
	// A private prefix-only local edit advances the public revision, without disclosing it.
	data = append([]byte("# keep operator comment\n"), bytes.ReplaceAll(data, []byte(prefix), []byte("/secret-second"))...)
	os.WriteFile(p.Webhook, data, 0600)
	changed, err := m.Observe()
	if err != nil || changed.Revision == snapshot.Revision {
		t.Fatal("private edit did not advance revision")
	}
	stale := queue(t, snapshot, "")
	stale.Verb = &v
	receipt, err = m.Apply(stale)
	if err != nil || receipt.Status != syncwire.ReceiptStatusConflict {
		t.Fatal("stale private revision accepted")
	}
	next := queue(t, changed, "")
	next.Verb = &v
	prefix = "/replacement"
	receipt, err = m.Apply(next)
	if err != nil || receipt.Status != syncwire.ReceiptStatusSaved {
		t.Fatal("applied before daemon reload")
	}
	data, _ = os.ReadFile(p.Webhook)
	if !bytes.Contains(data, []byte("# keep operator comment")) {
		t.Fatal("lost comment")
	}
	if daemon.Reload() != nil {
		t.Fatal("reload")
	}
	receipt, err = m.Apply(next)
	if err != nil || receipt.Status != syncwire.ReceiptStatusApplied {
		t.Fatal("no activation")
	}
	os.WriteFile(p.Webhook, []byte("bad = ["), 0600)
	if _, err = m.Observe(); err == nil {
		t.Fatal("invalid edit accepted")
	}
	last, _ := m.Latest()
	if last.Configuration.Webhook.Domain != "https://receiver.example" {
		t.Fatal("lost last good projection")
	}
}

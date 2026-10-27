package main

import (
	"callmemaybe/internal/activation"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/syncmachine"
	"callmemaybe/internal/syncwire"
	"callmemaybe/internal/webhookconfig"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebhookSavedSurvivesRestartUntilDaemonConfirms(t *testing.T) {
	dir, err := os.MkdirTemp("", "cmm")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	paths := syncmachine.Paths{Policy: filepath.Join(dir, "policy.toml"), Handsets: filepath.Join(dir, "handsets.toml"), Webhook: filepath.Join(dir, "webhook.toml")}
	if os.WriteFile(paths.Policy, []byte(goodPolicy), 0600) != nil || os.WriteFile(paths.Handsets, []byte(goodHandsets), 0600) != nil {
		t.Fatal("fixture")
	}
	files, _ := policy.DiscoverLines(paths.Policy)
	opened, err := openLines(files, paths.Handsets, filepath.Join(dir, "trunks.toml"), true, quiet(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, line := range opened {
			line.store.Close()
		}
	}()
	box := "ac409cd3-8105-4200-9a32-42a064a98bb0"
	state := filepath.Join(dir, "sync")
	m, err := syncmachine.Open(state, box, paths)
	if err != nil {
		t.Fatal(err)
	}
	daemon := webhookconfig.OpenStore(paths.Webhook)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		activation.Serve(ctx, state, func() map[string]string { return activationDigests(opened, nil, daemon) })
	}()
	defer func() { cancel(); <-done }()
	settle(t, "activation socket", func() bool { _, err := activation.Read(state); return err == nil })
	snapshot, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	m.Applied = activationApplied(state)
	m.OpenWebhook = func(string, syncwire.QueueItem) (webhookconfig.Config, error) {
		return webhookconfig.Config{Domain: "https://receiver.example", PathPrefix: "/private"}, nil
	}
	v := syncwire.Verb{}
	_ = v.FromVerb9(syncwire.Verb9{Name: "webhook.set", Domain: "https://receiver.example", Envelope: syncwire.WebhookEnvelope{EphemeralKey: strings.Repeat("A", 43), Nonce: strings.Repeat("A", 16), Ciphertext: "encrypted"}})
	var item syncwire.QueueItem
	// Typed construction through the public decoder keeps the fixture on the real wire contract.
	raw := `{"id":"6f1c1d4e-3c39-4a55-9a51-7c1b1f0c2a10","base":{"generation":"` + snapshot.Generation + `","revision":"` + snapshot.Revision + `"},"notAfter":9999999999999,"createdAt":1,"author":{"kind":"person","person":"0b6b1d2e-5f4a-4c3b-8a29-1e0d9c8b7a65"},"verb":{"name":"backup.run"}}`
	if syncwire.Decode("QueueItem", []byte(raw), &item) != nil {
		t.Fatal("decode")
	}
	item.NotAfter = time.Now().Add(time.Hour).UnixMilli()
	item.Verb = &v
	receipt, err := m.Apply(item)
	if err != nil || receipt.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("before reload: %v %v", receipt, err)
	}
	if m.Close() != nil {
		t.Fatal("close")
	}
	m, err = syncmachine.Open(state, box, paths)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.Applied = activationApplied(state)
	// A restarted sync process recognizes the durable write. It must not decrypt or write again.
	receipt, err = m.Apply(item)
	if err != nil || receipt.Status != syncwire.ReceiptStatusSaved {
		t.Fatal("restart lost saved receipt")
	}
	if daemon.Reload() != nil {
		t.Fatal("daemon reload")
	}
	receipt, err = m.Apply(item)
	if err != nil || receipt.Status != syncwire.ReceiptStatusApplied {
		t.Fatalf("after reload: %v %v", receipt, err)
	}
	if activationDigests(nil, nil, daemon)[webhookconfig.ActivationKey] == "" {
		t.Fatal("no webhook proof")
	}
}

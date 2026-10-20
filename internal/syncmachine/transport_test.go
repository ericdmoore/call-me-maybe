package syncmachine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"callmemaybe/internal/syncwire"
)

func TestRoundTripPublishesLocalEditsAndReplaysLostReceipt(t *testing.T) {
	m, p, _ := fixture(t)
	s, _ := m.Observe()
	q := queue(t, s, "policy.setSchedule")
	m.Applied = func(Paths, map[string][]byte) bool { return true }
	links := map[string]any{"self": map[string]any{"href": "/v1/sync/"}, "up": map[string]any{"href": "/v1/"}}
	var posted []syncwire.Snapshot
	var receipt syncwire.Receipt
	lost := true
	transport := func(ctx context.Context, endpoint string, body []byte) ([]byte, error) {
		var result any
		switch endpoint {
		case "snapshot":
			var current syncwire.Snapshot
			if err := syncwire.Decode("Snapshot", body, &current); err != nil {
				t.Fatal(err)
			}
			posted = append(posted, current)
			result = map[string]any{"status": "accepted", "generation": current.Generation, "revision": current.Revision, "_links": links}
		case "queue":
			result = map[string]any{"items": []syncwire.QueueItem{q}, "pollAfterMs": 5000, "_links": links}
		case "receipts":
			var batch syncwire.ReceiptBatch
			if err := syncwire.Decode("ReceiptBatch", body, &batch); err != nil {
				t.Fatal(err)
			}
			receipt = batch.Receipts[0]
			if lost {
				lost = false
				return nil, errors.New("connection lost after accepting receipt")
			}
			result = map[string]any{"receipts": []any{map[string]any{"id": q.Id, "status": "duplicate"}}, "_links": links}
		default:
			t.Fatal("unexpected transport endpoint")
		}
		return json.Marshal(result)
	}
	if m.Exchange(context.Background(), transport) == nil {
		t.Fatal("lost connection reported success")
	}
	content, _ := os.ReadFile(p.Policy)
	if err := m.Exchange(context.Background(), transport); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p.Policy)
	if string(after) != string(content) || receipt.Status != syncwire.ReceiptStatusApplied {
		t.Fatal("lost receipt repeated or lost the effect")
	}
	if len(posted) != 4 || posted[0].Revision == posted[1].Revision || posted[1].Revision != posted[3].Revision {
		t.Fatal("resulting snapshot did not return to service")
	}
}

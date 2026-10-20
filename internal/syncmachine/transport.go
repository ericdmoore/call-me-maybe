package syncmachine

import (
	"context"
	"encoding/json"

	"callmemaybe/internal/syncwire"
)

type Transport func(context.Context, string, []byte) ([]byte, error)

func exchange(ctx context.Context, t Transport, endpoint string, value any, responseName string, target any) error {
	var body []byte
	if value != nil {
		var err error
		body, err = json.Marshal(value)
		if err != nil {
			return syncwire.ErrInvalid
		}
	}
	reply, err := t(ctx, endpoint, body)
	if err != nil {
		return err
	}
	return syncwire.Decode(responseName, reply, target)
}

// Exchange makes one bounded, reconnectable round trip. The watcher runs
// independently; slow network I/O cannot prevent local changes entering SQLite.
func (m *Machine) Exchange(ctx context.Context, t Transport) error {
	s, err := m.Observe()
	if err != nil {
		// Still answer pending work against the last good snapshot. Apply will
		// refuse invalid current files rather than replacing them.
		s, err = m.Latest()
	}
	if err != nil {
		return err
	}
	var accepted syncwire.SnapshotResult
	if err = exchange(ctx, t, "snapshot", s, "SnapshotResult", &accepted); err != nil {
		return err
	}
	var queue syncwire.QueueResult
	if err = exchange(ctx, t, "queue", nil, "QueueResult", &queue); err != nil {
		return err
	}
	for _, item := range queue.Items {
		receipt, err := m.Apply(item)
		if err != nil {
			return err
		}
		// Publish the resulting revision before completing the queue slot. A
		// succeeding UI edit must be based on the actual new configuration.
		latest, err := m.Latest()
		if err != nil {
			return err
		}
		if err = exchange(ctx, t, "snapshot", latest, "SnapshotResult", &accepted); err != nil {
			return err
		}
		var receipts syncwire.ReceiptResults
		if err = exchange(ctx, t, "receipts", syncwire.ReceiptBatch{Receipts: []syncwire.Receipt{receipt}}, "ReceiptResults", &receipts); err != nil {
			return err
		}
		if len(receipts.Receipts) != 1 || receipts.Receipts[0].Id != item.Id || receipts.Receipts[0].Status == syncwire.ReceiptResultStatusConflict {
			return syncwire.ErrInvalid
		}
	}
	if m.SampleStatus != nil {
		var result syncwire.StatusResult
		if err := exchange(ctx, t, "status", m.SampleStatus(), "StatusResult", &result); err != nil {
			return err
		}
	}
	return nil
}

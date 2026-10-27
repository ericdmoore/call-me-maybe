package syncmachine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"callmemaybe/internal/syncwire"
)

type Transport func(context.Context, string, []byte) ([]byte, error)

var ErrReceiptConflict = errors.New("sync: the service holds a different receipt for a command; continuing with the queue")

// ErrTransportEnded marks a transport error that no later pass can fix, such
// as the account having signed this box out. Exchange returns it at once,
// even from the informational status report, so the caller can stop with one
// clear line instead of a warning followed by the same refusal.
var ErrTransportEnded = errors.New("sync: the account transport has ended")

// ErrBadRequest marks a transport error that is the service refusing the
// request as malformed (HTTP 400): retrying the same body cannot succeed.
// The transport wraps it; reportStatus uses it to tell "the service does not
// accept the box name" from a network failure.
var ErrBadRequest = errors.New("sync: the service refused the request as invalid")

// nameRetryAfter is how long the box waits to offer its name again after a
// status report carrying it was refused as invalid: long enough that a
// service that does not know the field yet costs one extra request a day.
const nameRetryAfter = 24 * time.Hour

// republishAfter re-sends an unchanged snapshot occasionally, so a service that
// lost its copy recovers without waiting for a local edit or a restart.
const republishAfter = 15 * time.Minute

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

// warn reports a problem that does not stop the pass, once per change of
// message, so a refusal repeated every few seconds is one log line.
// Messages are categorical: transport errors never carry bodies or credentials.
func (m *Machine) warn(kind string, err error) {
	if m.warned == nil {
		m.warned = map[string]string{}
	}
	if err == nil {
		delete(m.warned, kind)
		return
	}
	if m.warned[kind] == err.Error() {
		return
	}
	m.warned[kind] = err.Error()
	if m.Warn != nil {
		m.Warn(err)
		return
	}
	fmt.Fprintln(os.Stderr, err)
}

// publish skips a snapshot the service has already accepted. Every pass used
// to re-send the whole configuration, which on a household uplink is most of
// the traffic this process makes.
func (m *Machine) publish(ctx context.Context, t Transport, s syncwire.Snapshot) error {
	key := s.Generation + "/" + s.Revision
	if key == m.published && m.now().Sub(m.publishedAt) < republishAfter {
		return nil
	}
	var accepted syncwire.SnapshotResult
	if err := exchange(ctx, t, "snapshot", s, "SnapshotResult", &accepted); err != nil {
		m.published = ""
		return err
	}
	if accepted.Generation == s.Generation && accepted.Revision == s.Revision {
		m.published, m.publishedAt = key, m.now()
	}
	return nil
}

// reportStatus sends one status report, carrying the suggested box name until
// the service has accepted a report with it for this enrolment. Acceptance is
// recorded in the sync state directory, so a restart does not offer it again.
//
// The name must never cost the rest of the report: a service that refuses the
// report carrying it as invalid (one that does not know the field yet, say)
// gets the same report again without it at once, and is offered the name
// again only after nameRetryAfter. Any other failure is the report's, not the
// name's, and the next pass offers it again.
func (m *Machine) reportStatus(ctx context.Context, t Transport, status syncwire.Status) error {
	name := m.BoxName
	if name == "" || m.nameAccepted || m.now().Before(m.nameRetryAt) {
		name = ""
	} else {
		status.BoxName = &name
	}
	var result syncwire.StatusResult
	err := exchange(ctx, t, "status", status, "StatusResult", &result)
	if name == "" {
		return err
	}
	if err == nil {
		// Decode admits only {"status":"accepted"}. A failed write only means
		// one more offer after a restart.
		m.nameAccepted = true
		_, _ = m.db.Exec("INSERT OR IGNORE INTO box_name VALUES (?,?)", m.box, m.now().UnixMilli())
		return nil
	}
	if !errors.Is(err, ErrBadRequest) || ctx.Err() != nil {
		return err
	}
	m.nameRetryAt = m.now().Add(nameRetryAfter)
	status.BoxName = nil
	return exchange(ctx, t, "status", status, "StatusResult", &result)
}

// Exchange makes one bounded, reconnectable round trip. The watcher runs
// independently; slow network I/O cannot prevent local changes entering SQLite.
// Exchange itself must not run concurrently with another Exchange.
func (m *Machine) Exchange(ctx context.Context, t Transport) error {
	// Observe first, so the status report carries this pass's verdict on the
	// files: configRejection is present exactly while the newest content is
	// refused.
	s, err := m.Observe()
	// Presence is independent of whether the local configuration can currently
	// be parsed. An invalid edit must not make a reachable box appear offline.
	// Status is informational too: a refused report must not hold the queue.
	if m.SampleStatus != nil {
		status := m.SampleStatus()
		status.ConfigRejection = m.Rejection()
		err := m.reportStatus(ctx, t, status)
		if errors.Is(err, ErrTransportEnded) {
			return err
		}
		if err != nil {
			err = fmt.Errorf("sync: status report not accepted: %w", err)
		}
		m.warn("status", err)
	}
	if err != nil {
		// Still answer pending work against the last good snapshot. Apply will
		// refuse invalid current files rather than replacing them.
		s, err = m.Latest()
	}
	if err != nil {
		return err
	}
	if err = m.publish(ctx, t, s); err != nil {
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
		if err = m.publish(ctx, t, latest); err != nil {
			return err
		}
		var receipts syncwire.ReceiptResults
		if err = exchange(ctx, t, "receipts", syncwire.ReceiptBatch{Receipts: []syncwire.Receipt{receipt}}, "ReceiptResults", &receipts); err != nil {
			return err
		}
		if len(receipts.Receipts) != 1 || receipts.Receipts[0].Id != item.Id {
			return syncwire.ErrInvalid
		}
		if receipts.Receipts[0].Status == syncwire.ReceiptResultStatusConflict {
			// One command's disagreement is not a reason to stop answering the
			// others; the service already holds its answer for this one.
			m.warn("receipt", ErrReceiptConflict)
		}
	}
	return nil
}

package syncmachine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/syncwire"
)

const acceptedStatus = `{"status":"accepted","_links":{"self":{"href":"/v1/sync/status"},"up":{"href":"/v1/sync/"}}}`

// statusOnly answers the status report with accept (or refuse) and ends the
// pass at the snapshot, recording each report's boxName ("" when absent).
func statusOnly(t *testing.T, names *[]string, refuse func(syncwire.Status) error) Transport {
	return func(_ context.Context, endpoint string, body []byte) ([]byte, error) {
		if endpoint != "status" {
			return nil, errors.New("stop after status")
		}
		var status syncwire.Status
		if err := syncwire.Decode("Status", body, &status); err != nil {
			t.Fatalf("status report off the wire schema: %v", err)
		}
		name := ""
		if status.BoxName != nil {
			name = *status.BoxName
		}
		*names = append(*names, name)
		if refuse != nil {
			if err := refuse(status); err != nil {
				return nil, err
			}
		}
		return []byte(acceptedStatus), nil
	}
}

func sampled() syncwire.Status {
	return syncwire.Status{Schema: 1, Version: "test", SampledAt: time.Now().UnixMilli()}
}

func TestStatusCarriesTheBoxNameUntilAcceptedAcrossRestarts(t *testing.T) {
	m, p, state := fixture(t)
	m.SampleStatus, m.BoxName = sampled, "kitchen-pi"
	var names []string
	send := statusOnly(t, &names, nil)
	for range 2 {
		_ = m.Exchange(context.Background(), send)
	}
	if fmt.Sprint(names) != "[kitchen-pi ]" {
		t.Fatalf("reports: %q", names)
	}
	// A restart reads the acceptance back from the sync state directory.
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(state, "ac409cd3-8105-4200-9a32-42a064a98bb0", p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	again.SampleStatus, again.BoxName = sampled, "kitchen-pi"
	_ = again.Exchange(context.Background(), send)
	if fmt.Sprint(names) != "[kitchen-pi  ]" {
		t.Fatalf("name offered again after restart: %q", names)
	}
}

func TestAnEmptyBoxNameIsNeverSent(t *testing.T) {
	m, _, _ := fixture(t)
	m.SampleStatus = sampled
	var names []string
	_ = m.Exchange(context.Background(), statusOnly(t, &names, nil))
	if fmt.Sprint(names) != "[]" {
		t.Fatalf("reports: %q", names)
	}
	if m.nameAccepted {
		t.Fatal("a report without a name recorded one as accepted")
	}
}

// A service that refuses the report carrying the name as invalid (one with a
// strict schema that predates the field) still gets the rest of the report in
// the same pass, and is not offered the name again until nameRetryAfter.
func TestARefusedNameDoesNotCostTheReport(t *testing.T) {
	m, _, _ := fixture(t)
	now := time.Now()
	m.now = func() time.Time { return now }
	m.SampleStatus, m.BoxName = sampled, "kitchen-pi"
	var names []string
	refusing := true
	send := statusOnly(t, &names, func(s syncwire.Status) error {
		if refusing && s.BoxName != nil {
			return fmt.Errorf("%w: service refused request (400, bad_request)", ErrBadRequest)
		}
		return nil
	})
	_ = m.Exchange(context.Background(), send)
	if fmt.Sprint(names) != "[kitchen-pi ]" || m.warned["status"] != "" {
		t.Fatalf("reports %q, warned %v", names, m.warned)
	}
	_ = m.Exchange(context.Background(), send)
	if len(names) != 3 || names[2] != "" {
		t.Fatalf("name retried before its pause ended: %q", names)
	}
	now = now.Add(nameRetryAfter - time.Minute)
	_ = m.Exchange(context.Background(), send)
	if len(names) != 4 || names[3] != "" {
		t.Fatalf("name retried before a day passed: %q", names)
	}
	refusing = false
	now = now.Add(time.Minute)
	_ = m.Exchange(context.Background(), send)
	_ = m.Exchange(context.Background(), send)
	if strings.Join(names[4:], ",") != "kitchen-pi," {
		t.Fatalf("after the pause: %q", names)
	}
}

func TestAnEndedTransportDoesNotRetryWithoutTheName(t *testing.T) {
	m, _, _ := fixture(t)
	m.SampleStatus, m.BoxName = sampled, "kitchen-pi"
	calls := 0
	err := m.Exchange(context.Background(), func(context.Context, string, []byte) ([]byte, error) {
		calls++
		return nil, fmt.Errorf("%w: signed out", ErrTransportEnded)
	})
	if !errors.Is(err, ErrTransportEnded) || calls != 1 || m.nameAccepted {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

// Acceptance belongs to an enrolment: a ledger for another box id is a
// different enrolment and has recorded nothing.
func TestBoxNameAcceptanceIsPerEnrolment(t *testing.T) {
	m, _, _ := fixture(t)
	m.SampleStatus, m.BoxName = sampled, "kitchen-pi"
	var names []string
	_ = m.Exchange(context.Background(), statusOnly(t, &names, nil))
	var rows int
	if err := m.db.QueryRow("SELECT COUNT(*) FROM box_name WHERE box=?", "ac409cd3-8105-4200-9a32-42a064a98bb0").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("acceptance not recorded for this box: %d %v", rows, err)
	}
	if err := m.db.QueryRow("SELECT COUNT(*) FROM box_name WHERE box!=?", "ac409cd3-8105-4200-9a32-42a064a98bb0").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("acceptance recorded for another box: %d %v", rows, err)
	}
}

// Any other failure (the network, a 5xx) is the report's, not the name's:
// nothing is resent without the name, and the next pass offers it again.
func TestAFailedReportKeepsOfferingTheName(t *testing.T) {
	m, _, _ := fixture(t)
	m.SampleStatus, m.BoxName = sampled, "kitchen-pi"
	var names []string
	failing := true
	send := statusOnly(t, &names, func(syncwire.Status) error {
		if failing {
			return errors.New("sync: network unavailable")
		}
		return nil
	})
	_ = m.Exchange(context.Background(), send)
	if fmt.Sprint(names) != "[kitchen-pi]" || m.warned["status"] == "" {
		t.Fatalf("reports %q, warned %v", names, m.warned)
	}
	failing = false
	_ = m.Exchange(context.Background(), send)
	_ = m.Exchange(context.Background(), send)
	if fmt.Sprint(names) != "[kitchen-pi kitchen-pi ]" {
		t.Fatalf("reports %q", names)
	}
}

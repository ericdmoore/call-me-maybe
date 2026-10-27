package account

import (
	"context"
	"net/http"
	"time"
)

// SyncEndpoint is a closed transport surface. A caller cannot forward the box's
// credential to an arbitrary URL, and never receives the credential itself.
type SyncEndpoint string

const (
	SyncSnapshot SyncEndpoint = "snapshot"
	SyncQueue    SyncEndpoint = "queue"
	SyncReceipts SyncEndpoint = "receipts"
	SyncStatus   SyncEndpoint = "status"
)

// syncLockWait bounds how long a sync request waits for another account
// command's brief state access. A backup upload holds the lock for its whole
// transfer; the sync loop then sees ErrBusy and retries on its own schedule.
const syncLockWait = 2 * time.Second

// Sync holds the account lock only to read the credential and to settle its
// outcome, never across the network: a slow sync server must not make the
// nightly backup find the store busy.
func (c *Client) Sync(ctx context.Context, endpoint SyncEndpoint, data []byte) ([]byte, error) {
	method := http.MethodPost
	switch endpoint {
	case SyncSnapshot:
		method = http.MethodPut
	case SyncQueue:
		if len(data) != 0 {
			return nil, ErrProtocol
		}
		method = http.MethodGet
	case SyncReceipts, SyncStatus:
	default:
		return nil, ErrProtocol
	}
	if len(data) > 1<<20 {
		return nil, ErrProtocol
	}
	box, used, token, err := c.syncCredential(ctx)
	if err != nil {
		return nil, err
	}
	status, headers, body, err := c.request(ctx, method, "/v1/sync/"+string(endpoint), token, data, 2<<20)
	if err != nil {
		return nil, err
	}
	if removed(status, body) {
		applied, err := c.settleRemoved(ctx, box)
		if err != nil {
			return nil, err
		}
		if !applied {
			// The removal was about an enrolment that is gone. Do not hand
			// the caller a removal: it would stop sync for the new box.
			return nil, ErrSuperseded
		}
	} else if status == http.StatusUnauthorized || (status >= 200 && status < 300 && used.Pending != "") {
		if err := c.settleSync(ctx, used, status); err != nil {
			return nil, err
		}
	}
	if status != http.StatusOK {
		return nil, failure(status, headers, body)
	}
	return body, nil
}

func (c *Client) syncCredential(ctx context.Context) (string, credential, string, error) {
	s, err := openStoreWait(ctx, c.StateDir, syncLockWait)
	if err != nil {
		return "", credential{}, "", err
	}
	defer s.close()
	v, err := s.load()
	if err != nil {
		return "", credential{}, "", err
	}
	if !v.RemovedAt.IsZero() {
		return "", credential{}, "", ErrRemoved
	}
	t := v.Tokens["sync"]
	token := t.Current
	if t.Pending != "" {
		token = t.Pending
	}
	if v.NeedsLogin || !validToken(token, "sync") {
		return "", credential{}, "", ErrLogin
	}
	return v.Box, t, token, nil
}

// settleRemoved records the owner's removal unless the store now belongs to
// a different enrolment: a doorman login that finished while this request
// was on the network made a new box, which the old box's removal must not
// touch. A rotation keeps the box, and the service revoked its tokens too.
// It reports whether the removal describes the stored enrolment (recorded
// now, or already recorded by another process); false means it is stale.
func (c *Client) settleRemoved(ctx context.Context, box string) (bool, error) {
	s, err := openStoreWait(ctx, c.StateDir, syncLockWait)
	if err != nil {
		return false, err
	}
	defer s.close()
	v, err := s.load()
	if err != nil {
		return false, err
	}
	if v.Box != box {
		return false, nil
	}
	if !v.RemovedAt.IsZero() {
		return true, nil
	}
	return true, c.markRemoved(s, v)
}

// settleSync applies the same promotion and revocation rules as
// authenticatedVia, but only if the stored sync credential is still the one
// this request used. Anything else (a rotation, a fresh login, another process
// that already settled it) happened after the request was sent and wins; the
// rest of the file is re-read so a concurrent Upload's state is never lost.
func (c *Client) settleSync(ctx context.Context, used credential, status int) error {
	s, err := openStoreWait(ctx, c.StateDir, syncLockWait)
	if err != nil {
		return err
	}
	defer s.close()
	v, err := s.load()
	if err != nil {
		return err
	}
	if v.Tokens["sync"] != used {
		return nil
	}
	if status == http.StatusUnauthorized {
		// Never fall back to the predecessor: an ambiguous first use may
		// already have committed the successor on the server.
		v.NeedsLogin = true
	} else {
		v.Tokens["sync"] = credential{Current: used.Pending}
	}
	return s.save(v)
}

package account

import (
	"context"
	"net/http"
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
	s, err := openStore(c.StateDir)
	if err != nil {
		return nil, err
	}
	defer s.close()
	v, err := s.load()
	if err != nil {
		return nil, err
	}
	status, headers, body, err := c.authenticatedVia(s, v, "sync", func(token string) (int, http.Header, []byte, error) {
		return c.request(ctx, method, "/v1/sync/"+string(endpoint), token, data, 2<<20)
	})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, failure(status, headers, body)
	}
	return body, nil
}
